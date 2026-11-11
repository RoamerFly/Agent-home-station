package station

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/workspacefs"
)

const (
	douyinMemoryWorks     = "works"
	douyinMemoryComments  = "comments"
	douyinMemoryNotices   = "notices"
	localDouyinMemoryTime = "2006-01-02T15:04:05.000000000Z"
)

var localDouyinMemoryKinds = [...]string{douyinMemoryWorks, douyinMemoryComments, douyinMemoryNotices}

type localDouyinMemoryGrants struct {
	Owner    string `json:"owner,omitempty"`
	Works    string `json:"works,omitempty"`
	Comments string `json:"comments,omitempty"`
	Notices  string `json:"notices,omitempty"`
}

func (g *localDouyinMemoryGrants) slot(kind string) *string {
	switch kind {
	case douyinMemoryWorks:
		return &g.Works
	case douyinMemoryComments:
		return &g.Comments
	case douyinMemoryNotices:
		return &g.Notices
	default:
		return nil
	}
}

func (g localDouyinMemoryGrants) token(kind string) string {
	if slot := g.slot(kind); slot != nil {
		return *slot
	}
	return ""
}

func (g localDouyinMemoryGrants) valid() bool {
	if g == (localDouyinMemoryGrants{}) {
		return true
	}
	if !identity.ValidPathID(g.Owner) {
		return false
	}
	for _, kind := range localDouyinMemoryKinds {
		if token := g.token(kind); token != "" && !validLocalDouyinWebID(token) {
			return false
		}
	}
	return true
}

func (r localDouyinWebRecord) memorySourceAllowed(kind string) bool {
	switch kind {
	case douyinMemoryWorks:
		return r.WorksEnabled
	case douyinMemoryComments:
		return r.WorksEnabled && r.ReadEnabled && localDouyinWebScope(r) == "home_and_videos" && r.CommentsEnabled
	case douyinMemoryNotices:
		return r.NoticeAgent && r.NoticeAccount != ""
	default:
		return false
	}
}

func (r *localDouyinWebRecord) normalizeMemoryGrants(previous localDouyinWebRecord) {
	if r.Detached || r.Profile != previous.Profile || r.ProfileEpoch != previous.ProfileEpoch || r.ConfirmedAt == "" {
		r.Memory = localDouyinMemoryGrants{}
		return
	}
	for _, kind := range localDouyinMemoryKinds {
		if !r.memorySourceAllowed(kind) {
			*r.Memory.slot(kind) = ""
		}
	}
	if r.NoticeAccount != previous.NoticeAccount {
		r.Memory.Notices = ""
	}
	if r.Memory.Works == "" && r.Memory.Comments == "" && r.Memory.Notices == "" {
		r.Memory = localDouyinMemoryGrants{}
	}
}

type localDouyinMemorySource struct {
	Kind    string `json:"kind"`
	Scope   string `json:"source_id"`
	binding string
}

func douyinMemorySource(record localDouyinWebRecord, fish, owner, kind string) (localDouyinMemorySource, bool) {
	token := record.Memory.token(kind)
	if token == "" || record.Memory.Owner != owner || record.FishID != fish || record.Paused || record.Detached ||
		record.ConfirmedAt == "" || record.Profile == "" || !record.memorySourceAllowed(kind) {
		return localDouyinMemorySource{}, false
	}
	binding := localCheckpointKey("douyin-creator", fish, owner, record.Profile, record.ProfileEpoch)
	if kind == douyinMemoryNotices {
		binding = localCheckpointKey("douyin-notice", fish, owner, record.Profile, record.ProfileEpoch, record.NoticeAccount, token)
	}
	return localDouyinMemorySource{Kind: kind, Scope: localCheckpointKey(binding, kind, token), binding: binding}, true
}

type localDouyinMemoryRecord struct {
	ID, Work, Label, Text, URL, Coverage, At string
}

const localDouyinMemorySchema = `
CREATE TABLE IF NOT EXISTS bindings (id TEXT PRIMARY KEY, account TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS generations (scope TEXT PRIMARY KEY, epoch TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS records (
  scope TEXT NOT NULL, id TEXT NOT NULL, work TEXT NOT NULL, label TEXT NOT NULL,
  text TEXT NOT NULL, url TEXT NOT NULL, coverage TEXT NOT NULL, observed_at TEXT NOT NULL,
  PRIMARY KEY (scope, id)
);
CREATE INDEX IF NOT EXISTS records_time ON records(scope, observed_at DESC, id);
CREATE TABLE IF NOT EXISTS audit (
  request TEXT NOT NULL, scope TEXT NOT NULL, kind TEXT NOT NULL,
  searches INTEGER NOT NULL, reads INTEGER NOT NULL, items INTEGER NOT NULL,
  estimated_tokens INTEGER NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(request, scope)
);`

func openLocalDouyinMemory(ctx context.Context, home *localHomeService, create bool) (*sql.DB, error) {
	path := filepath.Join(home.dataDir, "apps", "douyin", "memory")
	open := workspacefs.OpenPath
	if create {
		open = workspacefs.EnsurePath
	}
	root, err := open(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("observations.sqlite")
	if err != nil && (!create || !errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	if info != nil && !info.Mode().IsRegular() {
		return nil, apphost.ErrObservationWebStorageUnavailable
	}
	db, err := sql.Open("sqlite", filepath.Join(path, "observations.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err == nil && create {
		_, err = db.ExecContext(ctx, localDouyinMemorySchema)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Creator and notice accounts are checked against their own upstream proof.
// Different providers' account anchors are never compared or substituted.
func (m *localDouyinWebLogin) captureMemory(ctx context.Context, record localDouyinWebRecord, kind, account string, rows []localDouyinMemoryRecord) error {
	if record.Memory.token(kind) == "" && !(kind == douyinMemoryWorks && record.Memory.Comments != "") {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	owner, err := m.home.identity.Read()
	if err != nil || owner.LocalOwner == nil || owner.FishID != record.FishID {
		return apphost.ErrObservationForbidden
	}
	if owner.LocalOwner.ID != record.Memory.Owner {
		return nil
	}
	path := filepath.Join(m.home.dataDir, "apps", "douyin", "web-login", record.FishID)
	root, err := workspacefs.OpenPath(path)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := readLocalDouyinWebRecord(root, record.FishID)
	if err != nil || current != record {
		return apphost.ErrObservationForbidden
	}
	source, retain := douyinMemorySource(record, owner.FishID, owner.LocalOwner.ID, kind)
	if !retain {
		if kind != douyinMemoryWorks {
			return nil
		}
		source, retain = douyinMemorySource(record, owner.FishID, owner.LocalOwner.ID, douyinMemoryComments)
		if !retain {
			return nil
		}
		rows = nil // Only verify the creator binding when works retention is off.
	}
	db, err := openLocalDouyinMemory(ctx, m.home, true)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	accountHash := ""
	if account != "" && (kind != douyinMemoryNotices || record.NoticeAccount == account) {
		accountHash = localCheckpointKey(source.binding, account)
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO bindings(id,account) VALUES(?,?)", source.binding, accountHash); err != nil {
		return err
	}
	var pinned string
	if err = tx.QueryRowContext(ctx, "SELECT account FROM bindings WHERE id=?", source.binding).Scan(&pinned); err != nil {
		return err
	}
	if pinned != accountHash || accountHash == "" {
		if _, err = tx.ExecContext(ctx, "UPDATE bindings SET account='' WHERE id=?", source.binding); err != nil {
			return err
		}
		return tx.Commit()
	}
	changed := false
	for _, row := range rows {
		result, err := tx.ExecContext(ctx, `INSERT INTO records(scope,id,work,label,text,url,coverage,observed_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(scope,id) DO UPDATE SET work=excluded.work,label=excluded.label,text=excluded.text,url=excluded.url,coverage=excluded.coverage,observed_at=excluded.observed_at
WHERE work<>excluded.work OR label<>excluded.label OR text<>excluded.text OR url<>excluded.url OR coverage<>excluded.coverage`,
			source.Scope, row.ID, row.Work, row.Label, row.Text, row.URL, row.Coverage, row.At)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = changed || n > 0
	}
	if changed {
		if _, err = tx.ExecContext(ctx, "INSERT INTO generations(scope,epoch) VALUES(?,?) ON CONFLICT(scope) DO UPDATE SET epoch=excluded.epoch",
			source.Scope, identity.RandomToken()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *localDouyinWebLogin) captureNoticeMemory(ctx context.Context, record localDouyinWebRecord, page stationbrowser.DouyinNoticePage) error {
	if record.Memory.Notices == "" {
		return nil
	}
	now := time.Now().UTC().Format(localDouyinMemoryTime)
	rows := make([]localDouyinMemoryRecord, 0, len(page.Items)+1)
	for _, item := range page.Items {
		body := fmt.Sprintf("互动通知编号 %s\n类型 %d", item.ID, item.Type)
		if item.WorkID != "" {
			body += "\n关联作品 " + item.WorkID
		}
		if item.CreatedAt > 0 {
			body += "\n平台时间 " + time.Unix(item.CreatedAt, 0).UTC().Format(time.RFC3339)
		}
		rows = append(rows, localDouyinMemoryRecord{ID: "notice-" + item.ID, Work: item.WorkID, Label: "互动通知元数据",
			Text: body, URL: stationbrowser.DouyinNoticePageURL, Coverage: "已读取通知条目的元数据", At: now})
	}
	if text := strings.TrimSpace(page.RenderedText); text != "" {
		rows = append(rows, localDouyinMemoryRecord{ID: "rendered-" + localCheckpointKey(text), Label: "互动通知列表可见文字", Text: text,
			URL: stationbrowser.DouyinNoticePageURL, Coverage: "网页列表可见文字，可能含前批，无法逐条对应通知编号", At: now})
	}
	return m.captureMemory(ctx, record, douyinMemoryNotices, page.Account, rows)
}
