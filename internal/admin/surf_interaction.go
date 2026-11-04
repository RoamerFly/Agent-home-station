package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// surfInteractionRequest describes a deliberate public-platform action. The
// evidence and grant are immutable when an action is first planned.
type surfInteractionRequest struct {
	Account            string `json:"account"`
	Thread             string `json:"thread"`
	SessionID          string `json:"session_id"`
	CandidateID        string `json:"candidate_id"`
	Platform           string `json:"platform"`
	PlatformAccount    string `json:"platform_account"`
	Action             string `json:"action"`
	Text               string `json:"text"`
	ScopeKey           string `json:"scope_key"`
	InterestKey        string `json:"interest_key"`
	AuthorizationEpoch string `json:"authorization_epoch"`
	// GrantID names the configured source. Grant fingerprints its effective authorization.
	GrantID           string `json:"grant_id"`
	Grant             string `json:"grant"`
	SettingsRevision  int    `json:"settings_revision"`
	SourceURL         string `json:"source_url"`
	SourceJobID       string `json:"source_job_id"`
	SourceContentHash string `json:"source_content_hash"`
	SourceStart       int    `json:"source_start"`
	SourceEnd         int    `json:"source_end"`
}

// Receipt is trusted only when it echoes the host-issued RequestKey.
type surfInteractionReceipt struct {
	RequestKey  string `json:"request_key"`
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	Detail      string `json:"detail,omitempty"`
}
type surfInteraction struct {
	RequestKey  string                 `json:"request_key"`
	Request     surfInteractionRequest `json:"request"`
	State       string                 `json:"state"`
	ReceiptID   string                 `json:"receipt_id,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	ConfirmedAt time.Time              `json:"confirmed_at,omitempty"`
}

// SQLite provides process-reopen and cross-handle atomicity. It avoids a
// whole-history JSON rewrite and has a database-scoped receipt identity index.
type surfInteractionStore struct {
	path string
	mu   *sync.Mutex
}

// Multiple managers can reopen the same account while an old worker is draining.
// Serialize local opens without retaining account paths after removal.
var surfInteractionLocks [256]sync.Mutex

const surfInteractionSchema = `
CREATE TABLE IF NOT EXISTS surf_interactions(
 request_key TEXT PRIMARY KEY, account TEXT NOT NULL,
 thread TEXT NOT NULL, scope_key TEXT NOT NULL, interest_key TEXT NOT NULL,
 platform TEXT NOT NULL, platform_account TEXT NOT NULL, candidate_id TEXT NOT NULL, action TEXT NOT NULL, state TEXT NOT NULL,
 receipt_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 confirmed_at TEXT NOT NULL DEFAULT '', request_json BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS surf_interactions_confirmed ON surf_interactions(account,thread,scope_key,interest_key,state,confirmed_at DESC,request_key);
CREATE UNIQUE INDEX IF NOT EXISTS surf_interactions_one_unknown_action
 ON surf_interactions(account,platform,platform_account,candidate_id,action) WHERE state='submitted_unknown';
CREATE TABLE IF NOT EXISTS surf_interaction_receipts(
 account TEXT NOT NULL, platform TEXT NOT NULL, platform_account TEXT NOT NULL,
 operation_id TEXT NOT NULL, request_key TEXT NOT NULL UNIQUE,
 PRIMARY KEY(account,platform,platform_account,operation_id),
 FOREIGN KEY(request_key) REFERENCES surf_interactions(request_key)
);`

func newSurfInteractionStore(dir string) (*surfInteractionStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "surf-interactions.sqlite")
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("unsafe surf interaction database")
	}
	// Do not create or keep a SQLite handle for ordinary browsing. The ledger is
	// opened only by an authorized interaction operation, and closed afterwards.
	hash := sha256.Sum256([]byte(path))
	return &surfInteractionStore{path: path, mu: &surfInteractionLocks[hash[0]]}, nil
}

func (s *surfInteractionStore) open() (*sql.DB, error) {
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*sql.DB, error) { _ = db.Close(); return nil, e }
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", surfInteractionSchema} {
		if _, err := db.Exec(q); err != nil {
			return fail(err)
		}
	}
	if err := os.Chmod(s.path, 0600); err != nil {
		return fail(err)
	}
	return db, nil
}

func validSurfInteractionState(v string) bool {
	switch v {
	case "planned", "submitted_unknown", "accepted", "rejected", "blocked":
		return true
	}
	return false
}
func validSurfInteractionRequest(r surfInteractionRequest) bool {
	if r.Account == "" || r.Thread == "" || r.SessionID == "" || r.CandidateID == "" || r.Platform == "" || r.PlatformAccount == "" || r.Action != "comment" || r.ScopeKey == "" || r.InterestKey == "" || r.AuthorizationEpoch == "" || r.GrantID == "" || r.Grant == "" || r.SettingsRevision < 0 || r.SourceURL == "" || r.SourceJobID == "" || r.SourceContentHash == "" || r.SourceStart < 0 || r.SourceEnd < r.SourceStart {
		return false
	}
	for _, v := range []string{r.Account, r.Thread, r.SessionID, r.CandidateID, r.Platform, r.PlatformAccount, r.ScopeKey, r.InterestKey, r.AuthorizationEpoch, r.GrantID, r.Grant, r.SourceURL, r.SourceJobID, r.SourceContentHash} {
		if len(v) > 4096 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\x00\r\n") {
			return false
		}
	}
	return len(strings.TrimSpace(r.Text)) > 0 && len(r.Text) <= 4000 && !strings.ContainsRune(r.Text, '\x00')
}
func surfInteractionKey(r surfInteractionRequest) string {
	b, _ := json.Marshal(struct{ Account, Thread, SessionID, CandidateID, Platform, PlatformAccount, Action string }{r.Account, r.Thread, r.SessionID, r.CandidateID, r.Platform, r.PlatformAccount, r.Action})
	h := sha256.Sum256(append([]byte("surf-interaction-v1\x00"), b...))
	return "surf-interaction-" + hex.EncodeToString(h[:20])
}
func surfTime(t time.Time) string               { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func parseSurfTime(v string) (time.Time, error) { return time.Parse(time.RFC3339Nano, v) }

const surfInteractionColumns = `request_key,account,thread,scope_key,interest_key,platform,platform_account,candidate_id,action,state,receipt_id,created_at,updated_at,confirmed_at,request_json`

func scanSurfInteraction(row interface{ Scan(...any) error }) (surfInteraction, error) {
	var out surfInteraction
	var account, thread, scope, interest, platform, platformAccount, candidate, action string
	var raw []byte
	var created, updated, confirmed string
	if err := row.Scan(&out.RequestKey, &account, &thread, &scope, &interest, &platform, &platformAccount, &candidate, &action, &out.State, &out.ReceiptID, &created, &updated, &confirmed, &raw); err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out.Request); err != nil {
		return out, err
	}
	var err error
	if out.CreatedAt, err = parseSurfTime(created); err != nil {
		return out, err
	}
	if out.UpdatedAt, err = parseSurfTime(updated); err != nil {
		return out, err
	}
	if confirmed != "" {
		if out.ConfirmedAt, err = parseSurfTime(confirmed); err != nil {
			return out, err
		}
	}
	if out.RequestKey != surfInteractionKey(out.Request) || !validSurfInteractionRequest(out.Request) || !validSurfInteractionState(out.State) || account != out.Request.Account || thread != out.Request.Thread || scope != out.Request.ScopeKey || interest != out.Request.InterestKey || platform != out.Request.Platform || platformAccount != out.Request.PlatformAccount || candidate != out.Request.CandidateID || action != out.Request.Action {
		return out, errors.New("冲浪互动记录不完整")
	}
	return out, nil
}
func (s *surfInteractionStore) get(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (surfInteraction, error) {
	return scanSurfInteraction(q.QueryRowContext(ctx, `SELECT `+surfInteractionColumns+` FROM surf_interactions WHERE request_key=?`, key))
}

// plan returns the same key on an exact replay. A new trace can create a new
// intent after a confirmed action, but cannot submit while any prior action
// on the same platform candidate remains unknown.
func (s *surfInteractionStore) plan(request surfInteractionRequest) (surfInteraction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return surfInteraction{}, err
	}
	defer db.Close()
	if !validSurfInteractionRequest(request) {
		return surfInteraction{}, errors.New("冲浪互动请求无效")
	}
	key := surfInteractionKey(request)
	raw, _ := json.Marshal(request)
	now := surfTime(time.Now())
	_, err = db.Exec(`INSERT INTO surf_interactions(request_key,account,thread,scope_key,interest_key,platform,platform_account,candidate_id,action,state,created_at,updated_at,request_json) VALUES(?,?,?,?,?,?,?,?,?,'planned',?,?,?) ON CONFLICT(request_key) DO NOTHING`, key, request.Account, request.Thread, request.ScopeKey, request.InterestKey, request.Platform, request.PlatformAccount, request.CandidateID, request.Action, now, now, raw)
	if err != nil {
		return surfInteraction{}, err
	}
	row, err := s.get(context.Background(), db, key)
	if errors.Is(err, sql.ErrNoRows) {
		return surfInteraction{}, errors.New("冲浪互动记录缺失")
	}
	if err != nil {
		return surfInteraction{}, err
	}
	if row.Request != request && row.Request.SessionID == request.SessionID {
		return surfInteraction{}, errors.New("冲浪互动请求冲突；请查询原请求")
	}
	return row, nil
}

// claim atomically records submitted_unknown before the external call.
func (s *surfInteractionStore) claim(request surfInteractionRequest) (surfInteraction, bool, error) {
	row, err := s.plan(request)
	if err != nil {
		return surfInteraction{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return surfInteraction{}, false, err
	}
	defer db.Close()
	result, err := db.Exec(`UPDATE surf_interactions SET state='submitted_unknown',updated_at=? WHERE request_key=? AND state='planned'`, surfTime(time.Now()), row.RequestKey)
	if err != nil {
		// A different trace may have inserted its own planned row while an
		// earlier same-candidate request remains unknown. Resolve only within
		// the same authorized conversation; other scopes receive no key.
		var pendingKey string
		lookupErr := db.QueryRow(`SELECT request_key FROM surf_interactions WHERE account=? AND platform=? AND platform_account=? AND candidate_id=? AND action=? AND state='submitted_unknown'`, request.Account, request.Platform, request.PlatformAccount, request.CandidateID, request.Action).Scan(&pendingKey)
		_, _ = db.Exec(`DELETE FROM surf_interactions WHERE request_key=? AND state='planned'`, row.RequestKey)
		if lookupErr != nil {
			return surfInteraction{}, false, err
		}
		pending, lookupErr := s.get(context.Background(), db, pendingKey)
		if lookupErr != nil || pending.Request.Thread != request.Thread || pending.Request.ScopeKey != request.ScopeKey || pending.Request.InterestKey != request.InterestKey {
			return surfInteraction{}, false, errors.New("已有未确认的互动，暂不重复提交")
		}
		return pending, false, nil
	}
	n, _ := result.RowsAffected()
	row, err = s.get(context.Background(), db, row.RequestKey)
	return row, n == 1, err
}

// blockUnsubmitted is only for the worker that just won claim and then learns
// that authorization was revoked before it has called the adapter. It never
// touches a confirmed row and intentionally cannot revive a blocked action.
func (s *surfInteractionStore) blockUnsubmitted(key string) (surfInteraction, error) {
	if key == "" {
		return surfInteraction{}, errors.New("冲浪互动不存在")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return surfInteraction{}, err
	}
	defer db.Close()
	result, err := db.Exec(`UPDATE surf_interactions SET state='blocked',updated_at=? WHERE request_key=? AND state='submitted_unknown'`, surfTime(time.Now()), key)
	if err != nil {
		return surfInteraction{}, err
	}
	n, _ := result.RowsAffected()
	row, err := s.get(context.Background(), db, key)
	if errors.Is(err, sql.ErrNoRows) {
		return surfInteraction{}, errors.New("冲浪互动不存在")
	}
	if err != nil {
		return surfInteraction{}, err
	}
	if n == 0 && row.State != "blocked" {
		return surfInteraction{}, errors.New("冲浪互动已进入外部提交或已确认状态")
	}
	return row, nil
}

// settle accepts only an operation-specific receipt for a prior unknown claim.
func (s *surfInteractionStore) settle(key string, receipt surfInteractionReceipt) (surfInteraction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" || receipt.RequestKey != key {
		return surfInteraction{}, errors.New("冲浪互动回执不属于该请求")
	}
	db, err := s.open()
	if err != nil {
		return surfInteraction{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return surfInteraction{}, err
	}
	defer tx.Rollback()
	row, err := s.get(context.Background(), tx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return surfInteraction{}, errors.New("冲浪互动不存在")
	}
	if err != nil {
		return surfInteraction{}, err
	}
	if row.State == "accepted" || row.State == "rejected" {
		if !receiptBound(context.Background(), tx, row) {
			return surfInteraction{}, errors.New("冲浪互动回执索引不可读，原记录已保留")
		}
		if row.ReceiptID != receipt.OperationID || row.State != receipt.State {
			return surfInteraction{}, errors.New("冲浪互动回执与已确认记录不一致")
		}
		return row, tx.Commit()
	}
	if row.State == "blocked" {
		return row, tx.Commit()
	}
	if row.State != "submitted_unknown" {
		return surfInteraction{}, errors.New("冲浪互动尚未领取")
	}
	if receipt.State == "unknown" {
		return row, tx.Commit()
	}
	if (receipt.State != "accepted" && receipt.State != "rejected") || receipt.OperationID == "" || len(receipt.OperationID) > 1024 || strings.ContainsAny(receipt.OperationID, "\x00\r\n") {
		return surfInteraction{}, errors.New("冲浪互动回执无效")
	}
	now := surfTime(time.Now())
	if _, err = tx.Exec(`INSERT INTO surf_interaction_receipts(account,platform,platform_account,operation_id,request_key) VALUES(?,?,?,?,?)`, row.Request.Account, row.Request.Platform, row.Request.PlatformAccount, receipt.OperationID, key); err != nil {
		return surfInteraction{}, errors.New("冲浪互动回执已绑定其他请求")
	}
	r, err := tx.Exec(`UPDATE surf_interactions SET state=?,receipt_id=?,updated_at=?,confirmed_at=? WHERE request_key=? AND state='submitted_unknown'`, receipt.State, receipt.OperationID, now, now, key)
	if err != nil {
		return surfInteraction{}, err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return surfInteraction{}, errors.New("冲浪互动状态冲突")
	}
	if err = tx.Commit(); err != nil {
		return surfInteraction{}, err
	}
	row.State, row.ReceiptID = receipt.State, receipt.OperationID
	row.UpdatedAt, _ = parseSurfTime(now)
	row.ConfirmedAt = row.UpdatedAt
	return row, nil
}

func receiptBound(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, row surfInteraction) bool {
	var key string
	err := q.QueryRowContext(ctx, `SELECT request_key FROM surf_interaction_receipts WHERE account=? AND platform=? AND platform_account=? AND operation_id=?`, row.Request.Account, row.Request.Platform, row.Request.PlatformAccount, row.ReceiptID).Scan(&key)
	return err == nil && key == row.RequestKey
}
func (s *surfInteractionStore) read(key, account string) (surfInteraction, bool) {
	db, err := s.open()
	if err != nil {
		return surfInteraction{}, false
	}
	defer db.Close()
	row, err := s.get(context.Background(), db, key)
	return row, err == nil && row.Request.Account == account
}
func (s *surfInteractionStore) confirmed(account, thread, scope, interest string, limit int) []surfInteraction {
	if limit <= 0 {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT i.`+strings.ReplaceAll(surfInteractionColumns, ",", ",i.")+` FROM surf_interactions i JOIN surf_interaction_receipts r ON r.account=i.account AND r.platform=i.platform AND r.platform_account=i.platform_account AND r.operation_id=i.receipt_id AND r.request_key=i.request_key WHERE i.account=? AND i.thread=? AND i.scope_key=? AND i.interest_key=? AND i.state='accepted' ORDER BY i.confirmed_at DESC,i.request_key DESC LIMIT ?`, account, thread, scope, interest, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []surfInteraction{}
	for rows.Next() {
		row, e := scanSurfInteraction(rows)
		if e == nil && row.ReceiptID != "" && !row.ConfirmedAt.IsZero() {
			out = append(out, row)
			if len(out) >= limit {
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ConfirmedAt.After(out[j].ConfirmedAt) })
	return out
}

// pending exposes only durable unknown claims in the exact current scope. The
// caller must lookup these keys; it must not submit them again.
func (s *surfInteractionStore) pending(account, thread, scope, interest string, limit int) []surfInteraction {
	if limit <= 0 {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT `+surfInteractionColumns+` FROM surf_interactions WHERE account=? AND thread=? AND scope_key=? AND interest_key=? AND state='submitted_unknown' ORDER BY updated_at DESC,request_key DESC LIMIT ?`, account, thread, scope, interest, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []surfInteraction{}
	for rows.Next() {
		row, e := scanSurfInteraction(rows)
		if e == nil {
			out = append(out, row)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}
