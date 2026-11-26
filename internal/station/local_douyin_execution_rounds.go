package station

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const (
	localDouyinExecutionRoundsPath = "/api/local/apps/douyin/execution-rounds"
	localDouyinExecutionRoundLimit = 20
	localDouyinExecutionRoundBatch = 1000
)

var localDouyinExecutionZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// This ledger is deliberately limited to notification metadata. Neither a
// notification type nor a work ID proves whose comment it is or which comment
// it replies to. A reply decision needs the comment ID, parent comment ID,
// parent author and the current account's author identity from a verified
// source; none of those are available here.
type localDouyinExecutionRound struct {
	NoticeID      string                                 `json:"notice_id"`
	Type          int                                    `json:"type"`
	WorkID        string                                 `json:"work_id"`
	CreatedAt     string                                 `json:"created_at"`
	ObservedAt    string                                 `json:"observed_at"`
	State         string                                 `json:"state"`
	Reason        string                                 `json:"reason"`
	ReplyEligible bool                                   `json:"reply_eligible"`
	Candidates    []stationbrowser.DouyinNoticeCandidate `json:"candidates"`
}

type localDouyinExecutionRoundsResult struct {
	Date           string                      `json:"date"`
	Rounds         []localDouyinExecutionRound `json:"rounds"`
	NextBefore     string                      `json:"next_before"`
	EffectiveLimit int                         `json:"effective_limit"`
	Coverage       string                      `json:"coverage"`
}

const localDouyinExecutionSchema = `
CREATE TABLE IF NOT EXISTS rounds (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL,
  notice_id TEXT NOT NULL,
  type INTEGER NOT NULL,
  work_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  observed_date TEXT NOT NULL,
  UNIQUE(scope, notice_id)
);
CREATE INDEX IF NOT EXISTS rounds_day ON rounds(scope, observed_date, sequence DESC);
CREATE TABLE IF NOT EXISTS round_candidates (
  scope TEXT NOT NULL,
  notice_id TEXT NOT NULL,
  path TEXT NOT NULL,
  candidate_id TEXT NOT NULL,
  PRIMARY KEY(scope, notice_id, path, candidate_id)
) WITHOUT ROWID;`

func validDouyinRoundCandidate(candidate stationbrowser.DouyinNoticeCandidate) bool {
	switch candidate.Path {
	case "cid", "real_cid", "reply_id", "reply_to_reply_id", "user.uid",
		"comment.cid", "comment.reply_id", "comment.reply_to_reply_id", "comment.user.uid",
		"reply_comment.cid", "reply_comment.reply_id", "reply_comment.reply_to_reply_id", "reply_comment.user.uid":
	default:
		return false
	}
	if len(candidate.ID) < 1 || len(candidate.ID) > 20 || candidate.ID[0] == '0' {
		return false
	}
	for _, digit := range candidate.ID {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func localDouyinExecutionScope(fish, owner string, record localDouyinWebRecord) string {
	// The notice binding epoch prevents revocation and a later rebind of the
	// same account from reopening an older ledger. Other settings remain stable.
	return localCheckpointKey("douyin-execution-rounds-v1", fish, owner,
		record.ProfileEpoch, record.NoticeEpoch, record.NoticeAccount)
}

func openLocalDouyinExecutionDB(ctx context.Context, home *localHomeService, create bool) (*sql.DB, error) {
	path := filepath.Join(stationApplicationsDir(home.dataDir), "douyin", "execution-rounds")
	open := workspacefs.OpenPath
	if create {
		open = workspacefs.EnsurePath
	}
	root, err := open(path)
	if err != nil {
		if !create && errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("rounds.sqlite")
	if err != nil && (!create || !errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	if info != nil && !info.Mode().IsRegular() {
		return nil, errors.New("执行回合账本文件类型无效")
	}
	db, err := sql.Open("sqlite", filepath.Join(path, "rounds.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err == nil && create {
		if _, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err == nil {
			if _, err = db.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err == nil {
				_, err = db.ExecContext(ctx, localDouyinExecutionSchema)
			}
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (m *localDouyinWebLogin) captureExecutionRounds(ctx context.Context, fish, owner string, record localDouyinWebRecord, page stationbrowser.DouyinNoticePage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.FishID != fish || record.NoticeAccount == "" || page.Account != record.NoticeAccount || !validDouyinNoticeAccount(record.NoticeAccount) {
		return errLocalDouyinNoticeGrant
	}
	if len(page.Items) == 0 {
		return nil
	}
	current, _, err := m.noticeProfile(fish, record.Revision)
	if err != nil || current != record {
		return errLocalDouyinNoticeGrant
	}
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || identity.FishID != fish || identity.LocalOwner.ID != owner {
		return errLocalDouyinNoticeGrant
	}
	db, err := openLocalDouyinExecutionDB(ctx, m.home, true)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	observed := time.Now().UTC()
	stamp := observed.Format(time.RFC3339Nano)
	day := observed.In(localDouyinExecutionZone).Format("2006-01-02")
	scope := localDouyinExecutionScope(fish, owner, record)
	for _, item := range page.Items {
		if item.ID == "" {
			return errors.New("通知编号缺失，执行回合未保存")
		}
		created := ""
		if item.CreatedAt > 0 && item.CreatedAt < 253402300800 {
			created = time.Unix(item.CreatedAt, 0).UTC().Format(time.RFC3339)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO rounds(scope,notice_id,type,work_id,created_at,observed_at,observed_date) VALUES(?,?,?,?,?,?,?)`,
			scope, item.ID, item.Type, item.WorkID, created, stamp, day); err != nil {
			return err
		}
		if len(item.Candidates) > 16 {
			continue
		}
		seenCandidates := make(map[string]string, len(item.Candidates))
		valid := true
		for _, candidate := range item.Candidates {
			if !validDouyinRoundCandidate(candidate) {
				valid = false
				break
			}
			if previous, exists := seenCandidates[candidate.Path]; exists && previous != candidate.ID {
				valid = false
				break
			}
			seenCandidates[candidate.Path] = candidate.ID
		}
		if !valid {
			continue
		}
		for path, id := range seenCandidates {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO round_candidates(scope,notice_id,path,candidate_id) VALUES(?,?,?,?)`,
				scope, item.ID, path, id); err != nil {
				return err
			}
		}
	}
	// Match apply's lock order. Holding both the process lock and login.lock
	// across the final read and commit prevents a revocation save from crossing
	// between the authorization check and the durable ledger commit.
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := filepath.Join(stationApplicationsDir(m.home.dataDir), "douyin", "web-login", fish)
	unlock, err := hostfile.TryLock(filepath.Join(dir, "login.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err := m.ctx.Err(); err != nil {
		return err
	}
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		return errLocalDouyinNoticeGrant
	}
	defer root.Close()
	current, err = readLocalDouyinWebRecord(root, fish)
	if err != nil {
		return errLocalDouyinNoticeGrant
	}
	view := m.view(current)
	if current != record || current.NoticeAccount == "" ||
		current.Paused || current.Detached || !view.Available || view.State != "saved" || view.AttemptID != "" {
		return errLocalDouyinNoticeGrant
	}
	identity, err = m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || identity.FishID != fish || identity.LocalOwner.ID != owner {
		return errLocalDouyinNoticeGrant
	}
	return tx.Commit()
}

func (m *localDouyinWebLogin) executionRoundRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET "+localDouyinExecutionRoundsPath, owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		identity, err := m.home.identity.Read()
		if err != nil || identity.LocalOwner == nil || !fishidentity.ValidFishID(identity.FishID) {
			websession.Fail(w, http.StatusConflict, "请先准备本机主人身份")
			return
		}
		session, _, ok := owner.Lookup(r)
		if !ok {
			websession.Fail(w, http.StatusUnauthorized, "请重新登录工作站")
			return
		}
		query := r.URL.Query()
		if len(query) != 1 && len(query) != 2 && len(query) != 3 {
			websession.Fail(w, http.StatusBadRequest, "执行回合查询参数无效")
			return
		}
		for key, values := range query {
			if (key != "date" && key != "before" && key != "limit") || len(values) != 1 {
				websession.Fail(w, http.StatusBadRequest, "执行回合查询参数无效")
				return
			}
		}
		day := query.Get("date")
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil || parsed.Format("2006-01-02") != day {
			websession.Fail(w, http.StatusBadRequest, "请提供有效的 YYYY-MM-DD 日期")
			return
		}
		limit := localDouyinExecutionRoundLimit
		if values, explicit := query["limit"]; explicit {
			limit, err = strconv.Atoi(values[0])
			if err != nil || limit < 1 {
				websession.Fail(w, http.StatusBadRequest, "读取数量须为正整数")
				return
			}
		}
		if limit > localDouyinExecutionRoundBatch {
			websession.Fail(w, http.StatusBadRequest, "单次最多读取 1000 条执行回合，请使用 before 分页续读")
			return
		}
		var before int64
		if values, explicit := query["before"]; explicit {
			before, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || before < 1 {
				websession.Fail(w, http.StatusBadRequest, "续读位置无效")
				return
			}
		}
		record, _, err := m.noticeState(identity.FishID)
		if err != nil || record.NoticeAccount == "" {
			websession.Fail(w, http.StatusForbidden, "请先绑定并启用当前抖音互动通知账号")
			return
		}
		current := func() error {
			if token, _, ok := owner.Lookup(r); !ok || token != session {
				return errLocalDouyinNoticeGrant
			}
			actual, err := m.home.identity.Read()
			if err != nil || actual.LocalOwner == nil || actual.FishID != identity.FishID || actual.LocalOwner.ID != identity.LocalOwner.ID {
				return errLocalDouyinNoticeGrant
			}
			latest, _, err := m.noticeProfile(identity.FishID, record.Revision)
			if err != nil || latest != record || latest.NoticeAccount == "" {
				return errLocalDouyinNoticeGrant
			}
			return nil
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := current(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		result, err := m.listExecutionRounds(ctx, localDouyinExecutionScope(identity.FishID, identity.LocalOwner.ID, record), day, before, limit)
		if err != nil {
			websession.Fail(w, http.StatusServiceUnavailable, "执行回合账本读取失败")
			return
		}
		if err := current(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		websession.Respond(w, http.StatusOK, result)
	}))
}

func (m *localDouyinWebLogin) listExecutionRounds(ctx context.Context, scope, day string, before int64, limit int) (localDouyinExecutionRoundsResult, error) {
	result := localDouyinExecutionRoundsResult{Date: day, Rounds: []localDouyinExecutionRound{}, EffectiveLimit: limit, Coverage: "notice_metadata"}
	db, err := openLocalDouyinExecutionDB(ctx, m.home, false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer db.Close()
	var rows *sql.Rows
	if before == 0 {
		rows, err = db.QueryContext(ctx, `SELECT sequence,notice_id,type,work_id,created_at,observed_at FROM rounds WHERE scope=? AND observed_date=? ORDER BY sequence DESC LIMIT ?`, scope, day, limit+1)
	} else {
		rows, err = db.QueryContext(ctx, `SELECT sequence,notice_id,type,work_id,created_at,observed_at FROM rounds WHERE scope=? AND observed_date=? AND sequence<? ORDER BY sequence DESC LIMIT ?`, scope, day, before, limit+1)
	}
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		var row localDouyinExecutionRound
		var sequence int64
		if err := rows.Scan(&sequence, &row.NoticeID, &row.Type, &row.WorkID, &row.CreatedAt, &row.ObservedAt); err != nil {
			return result, err
		}
		if len(result.Rounds) == limit {
			result.NextBefore = strconv.FormatInt(last, 10)
			break
		}
		row.State, row.Reason = "待核对", "证据不足"
		row.Candidates = []stationbrowser.DouyinNoticeCandidate{}
		result.Rounds = append(result.Rounds, row)
		last = sequence
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	return result, readLocalDouyinRoundCandidates(ctx, db, scope, result.Rounds)
}

func readLocalDouyinRoundCandidates(ctx context.Context, db *sql.DB, scope string, rounds []localDouyinExecutionRound) error {
	if len(rounds) == 0 {
		return nil
	}
	// Older ledgers do not have this table. Viewing a round must remain read-only.
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='round_candidates'`).Scan(&tableCount); err != nil || tableCount == 0 {
		return err
	}
	indices := make(map[string]int, len(rounds))
	for index, row := range rounds {
		indices[row.NoticeID] = index
	}
	for start := 0; start < len(rounds); start += 200 {
		end := min(start+200, len(rounds))
		placeholders := make([]string, 0, end-start)
		args := make([]any, 0, end-start+1)
		args = append(args, scope)
		for _, row := range rounds[start:end] {
			placeholders = append(placeholders, "?")
			args = append(args, row.NoticeID)
		}
		query := `SELECT notice_id,path,candidate_id FROM round_candidates WHERE scope=? AND notice_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY notice_id,path,candidate_id`
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var noticeID string
			var candidate stationbrowser.DouyinNoticeCandidate
			if err := rows.Scan(&noticeID, &candidate.Path, &candidate.ID); err != nil {
				rows.Close()
				return err
			}
			index, found := indices[noticeID]
			if !found || !validDouyinRoundCandidate(candidate) || len(rounds[index].Candidates) >= 16 {
				rows.Close()
				return errors.New("抖音执行回合候选字段无效")
			}
			rounds[index].Candidates = append(rounds[index].Candidates, candidate)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}
