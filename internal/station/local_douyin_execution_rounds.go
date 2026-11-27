package station

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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
	localDouyinExecutionWorksPath  = localDouyinExecutionRoundsPath + "/works"
	localDouyinExecutionRoundLimit = 20
	localDouyinExecutionRoundBatch = 1000
)

var localDouyinExecutionZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// This ledger is deliberately limited to notification metadata. Neither a
// notification type nor a work ID proves whose comment it is or which comment
// it replies to. The private creator-account digest records the browser account
// at capture time, but does not prove work ownership or comment parentage.
// Reply decisions still require verified comment and parent identities.
type localDouyinExecutionRound struct {
	NoticeID         string                                 `json:"notice_id"`
	Type             int                                    `json:"type"`
	WorkID           string                                 `json:"work_id"`
	CreatedAt        string                                 `json:"created_at"`
	ObservedAt       string                                 `json:"observed_at"`
	State            string                                 `json:"state"`
	Reason           string                                 `json:"reason"`
	ReplyEligible    bool                                   `json:"reply_eligible"`
	Candidates       []stationbrowser.DouyinNoticeCandidate `json:"candidates"`
	DetailStatus     string                                 `json:"detail_status"`
	DetailCandidates []stationbrowser.DouyinNoticeCandidate `json:"detail_candidates"`
}

type localDouyinExecutionRoundsResult struct {
	Date           string                      `json:"date"`
	Rounds         []localDouyinExecutionRound `json:"rounds"`
	NextBefore     string                      `json:"next_before"`
	EffectiveLimit int                         `json:"effective_limit"`
	Coverage       string                      `json:"coverage"`
	GroupSelected  bool                        `json:"group_selected"`
	SelectedWorkID string                      `json:"selected_work_id"`
}

type localDouyinExecutionWorkGroup struct {
	WorkID      string `json:"work_id"`
	NoticeCount int    `json:"notice_count"`
	Ownership   string `json:"ownership"`
}

type localDouyinExecutionWorkGroupsResult struct {
	Date           string                          `json:"date"`
	Groups         []localDouyinExecutionWorkGroup `json:"groups"`
	NextCursor     string                          `json:"next_cursor"`
	EffectiveLimit int                             `json:"effective_limit"`
	Coverage       string                          `json:"coverage"`
}

type localDouyinExecutionWorksCursor struct {
	Anchor int64  `json:"anchor"`
	Before int64  `json:"before"`
	Date   string `json:"date"`
	Scope  string `json:"scope"`
}

var errLocalDouyinExecutionWorksCursor = errors.New("作品分组续页位置无效")

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
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS round_creator_accounts (
  scope TEXT NOT NULL,
  notice_id TEXT NOT NULL,
  creator_account TEXT NOT NULL,
  PRIMARY KEY(scope, notice_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS round_details (
  scope TEXT NOT NULL,
  notice_id TEXT NOT NULL,
  work_id TEXT NOT NULL,
  status TEXT NOT NULL,
  PRIMARY KEY(scope, notice_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS round_detail_candidates (
  scope TEXT NOT NULL,
  notice_id TEXT NOT NULL,
  path TEXT NOT NULL,
  candidate_id TEXT NOT NULL,
  PRIMARY KEY(scope, notice_id, path)
) WITHOUT ROWID;`

func validDouyinRoundCandidate(candidate stationbrowser.DouyinNoticeCandidate) bool {
	switch candidate.Path {
	case "cid", "real_cid", "reply_id", "reply_to_reply_id", "user.uid",
		"comment.cid", "comment.parent_id", "comment.reply_id", "comment.reply_to_reply_id", "comment.user.uid",
		"comment.comment.cid", "comment.comment.reply_id", "comment.comment.reply_to_reply_id",
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

func validDouyinRoundDetailStatus(status string) bool {
	switch status {
	case "not_observed", "partial", "in_flight", "invalid_response", "conflict",
		"account_unverified", "invalid_fields", "observed_no_candidates", "observed_candidate":
		return true
	}
	return false
}

func missingDouyinRoundDetailStatus(progress string) (string, bool) {
	switch progress {
	case "", "not_observed":
		return "not_observed", true
	case "partial", "in_flight", "invalid_response", "conflict":
		return progress, true
	}
	return "", false
}

func normalizedDouyinRoundDetail(item stationbrowser.DouyinNotice, detail stationbrowser.DouyinNoticeDetail, creatorAccount string) (string, map[string]string, error) {
	if detail.NoticeID != item.ID || item.WorkID != "" && detail.WorkID != "" && detail.WorkID != item.WorkID ||
		detail.RequestType != 0 && detail.RequestType != item.Type ||
		detail.NoticeType != 0 && detail.NoticeType != item.Type ||
		item.CreatedAt > 0 && detail.CreatedAt > 0 && item.CreatedAt != detail.CreatedAt {
		return "conflict", nil, nil
	}
	if item.WorkID == "" || detail.WorkID == "" || !validLocalDouyinCommentID(item.WorkID) {
		return "invalid_fields", nil, nil
	}
	if !detail.CandidateFieldsValid {
		return "invalid_fields", nil, nil
	}
	if creatorAccount == "" {
		return "account_unverified", nil, nil
	}
	if len(detail.Candidates) > 16 {
		return "invalid_fields", nil, nil
	}
	seen := make(map[string]string, len(detail.Candidates))
	for _, candidate := range detail.Candidates {
		if !validDouyinRoundCandidate(candidate) {
			return "invalid_fields", nil, nil
		}
		if prior, exists := seen[candidate.Path]; exists && prior != candidate.ID {
			return "conflict", nil, nil
		}
		seen[candidate.Path] = candidate.ID
	}
	if len(seen) == 0 {
		return "observed_no_candidates", nil, nil
	}
	return "observed_candidate", seen, nil
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
	if page.CreatorAccount != "" && !validDouyinNoticeAccount(page.CreatorAccount) {
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
	missingDetailStatus, validProgress := missingDouyinRoundDetailStatus(page.DetailProgress)
	if !validProgress {
		missingDetailStatus = "invalid_response"
	}
	itemsByID := make(map[string]stationbrowser.DouyinNotice, len(page.Items))
	for _, item := range page.Items {
		if item.ID == "" {
			return errors.New("通知编号缺失，执行回合未保存")
		}
		if _, duplicate := itemsByID[item.ID]; duplicate {
			return errLocalDouyinNoticeGrant
		}
		itemsByID[item.ID] = item
	}
	detailsByID := make(map[string]stationbrowser.DouyinNoticeDetail, len(page.Details))
	detailConflicts := make(map[string]bool)
	for _, detail := range page.Details {
		_, found := itemsByID[detail.NoticeID]
		if !found || detail.NoticeID == "" {
			missingDetailStatus = "conflict"
			continue
		}
		if _, duplicate := detailsByID[detail.NoticeID]; duplicate || detailConflicts[detail.NoticeID] {
			delete(detailsByID, detail.NoticeID)
			detailConflicts[detail.NoticeID] = true
			continue
		}
		detailsByID[detail.NoticeID] = detail
	}
	for _, item := range page.Items {
		created := ""
		if item.CreatedAt > 0 && item.CreatedAt < 253402300800 {
			created = time.Unix(item.CreatedAt, 0).UTC().Format(time.RFC3339)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO rounds(scope,notice_id,type,work_id,created_at,observed_at,observed_date) VALUES(?,?,?,?,?,?,?)`,
			scope, item.ID, item.Type, item.WorkID, created, stamp, day); err != nil {
			return err
		}
		var recordedType int
		var recordedWork string
		if err := tx.QueryRowContext(ctx, `SELECT type,work_id FROM rounds WHERE scope=? AND notice_id=?`, scope, item.ID).Scan(&recordedType, &recordedWork); err != nil || recordedType != item.Type || recordedWork != item.WorkID {
			return errLocalDouyinNoticeGrant
		}
		if page.CreatorAccount != "" {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO round_creator_accounts(scope,notice_id,creator_account) VALUES(?,?,?)`,
				scope, item.ID, page.CreatorAccount); err != nil {
				return err
			}
			var recorded string
			if err := tx.QueryRowContext(ctx, `SELECT creator_account FROM round_creator_accounts WHERE scope=? AND notice_id=?`, scope, item.ID).Scan(&recorded); err != nil {
				return err
			}
			if recorded != page.CreatorAccount {
				return errLocalDouyinNoticeGrant
			}
		}
		detailStatus, detailCandidates := missingDetailStatus, map[string]string(nil)
		if detailConflicts[item.ID] {
			detailStatus = "conflict"
		} else if detail, found := detailsByID[item.ID]; found {
			detailStatus, detailCandidates, err = normalizedDouyinRoundDetail(item, detail, page.CreatorAccount)
			if err != nil {
				return err
			}
		}
		if err := storeLocalDouyinRoundDetail(ctx, tx, scope, item.ID, item.WorkID, detailStatus, detailCandidates); err != nil {
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

func storeLocalDouyinRoundDetail(ctx context.Context, tx *sql.Tx, scope, noticeID, workID, status string, candidates map[string]string) error {
	if !validDouyinRoundDetailStatus(status) || status != "observed_candidate" && len(candidates) != 0 ||
		status == "observed_candidate" && len(candidates) == 0 {
		return errLocalDouyinNoticeGrant
	}
	var oldWork, oldStatus string
	err := tx.QueryRowContext(ctx, `SELECT work_id,status FROM round_details WHERE scope=? AND notice_id=?`, scope, noticeID).Scan(&oldWork, &oldStatus)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO round_details(scope,notice_id,work_id,status) VALUES(?,?,?,?)`, scope, noticeID, workID, status); err != nil {
			return err
		}
		return writeLocalDouyinRoundDetailCandidates(ctx, tx, scope, noticeID, candidates)
	}
	if err != nil || oldWork != workID || !validDouyinRoundDetailStatus(oldStatus) {
		return errLocalDouyinNoticeGrant
	}
	if oldStatus == "conflict" {
		return nil
	}
	oldObserved := oldStatus == "observed_candidate" || oldStatus == "observed_no_candidates"
	newObserved := status == "observed_candidate" || status == "observed_no_candidates"
	if oldObserved && newObserved {
		var oldCandidates map[string]string
		oldCandidates, err = readLocalDouyinDetailCandidateMap(ctx, tx, scope, noticeID)
		if err != nil {
			return err
		}
		if oldStatus == status && equalLocalDouyinCandidateMaps(oldCandidates, candidates) {
			return nil
		}
		status = "conflict"
		candidates = nil
	} else if oldObserved {
		if status == "invalid_fields" || status == "conflict" {
			status = "conflict"
			candidates = nil
		} else {
			return nil // a later missing detail cannot erase prior observed evidence
		}
	} else if status == "observed_candidate" || status == "observed_no_candidates" {
		if oldStatus == "invalid_fields" {
			status = "conflict"
			candidates = nil
		}
	} else if oldStatus == "invalid_fields" && status != "conflict" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE round_details SET status=? WHERE scope=? AND notice_id=?`, status, scope, noticeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM round_detail_candidates WHERE scope=? AND notice_id=?`, scope, noticeID); err != nil {
		return err
	}
	return writeLocalDouyinRoundDetailCandidates(ctx, tx, scope, noticeID, candidates)
}

func readLocalDouyinDetailCandidateMap(ctx context.Context, tx *sql.Tx, scope, noticeID string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path,candidate_id FROM round_detail_candidates WHERE scope=? AND notice_id=?`, scope, noticeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var candidate stationbrowser.DouyinNoticeCandidate
		if err := rows.Scan(&candidate.Path, &candidate.ID); err != nil || !validDouyinRoundCandidate(candidate) || len(result) >= 16 {
			return nil, errLocalDouyinNoticeGrant
		}
		result[candidate.Path] = candidate.ID
	}
	return result, rows.Err()
}

func equalLocalDouyinCandidateMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for path, id := range a {
		if b[path] != id {
			return false
		}
	}
	return true
}

func writeLocalDouyinRoundDetailCandidates(ctx context.Context, tx *sql.Tx, scope, noticeID string, candidates map[string]string) error {
	if len(candidates) > 16 {
		return errLocalDouyinNoticeGrant
	}
	for path, id := range candidates {
		if !validDouyinRoundCandidate(stationbrowser.DouyinNoticeCandidate{Path: path, ID: id}) {
			return errLocalDouyinNoticeGrant
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO round_detail_candidates(scope,notice_id,path,candidate_id) VALUES(?,?,?,?)`, scope, noticeID, path, id); err != nil {
			return err
		}
	}
	return nil
}

func (m *localDouyinWebLogin) executionRoundRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET "+localDouyinExecutionRoundsPath, owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		scope, current, ok := m.executionRoundReadScope(owner, w, r)
		if !ok {
			return
		}
		query := r.URL.Query()
		if len(query) < 1 || len(query) > 4 {
			websession.Fail(w, http.StatusBadRequest, "执行回合查询参数无效")
			return
		}
		for key, values := range query {
			if (key != "date" && key != "before" && key != "limit" && key != "work_id") || len(values) != 1 {
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
		var workFilter *string
		if values, explicit := query["work_id"]; explicit {
			if values[0] != "" && !validLocalDouyinCommentID(values[0]) {
				websession.Fail(w, http.StatusBadRequest, "作品编号无效")
				return
			}
			workFilter = &values[0]
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := current(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		result, err := m.listExecutionRoundsForWork(ctx, scope, day, before, limit, workFilter)
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
	mux.HandleFunc("GET "+localDouyinExecutionWorksPath, owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		scope, current, ok := m.executionRoundReadScope(owner, w, r)
		if !ok {
			return
		}
		query := r.URL.Query()
		if len(query) < 1 || len(query) > 3 {
			websession.Fail(w, http.StatusBadRequest, "作品分组查询参数无效")
			return
		}
		for key, values := range query {
			if (key != "date" && key != "limit" && key != "cursor") || len(values) != 1 {
				websession.Fail(w, http.StatusBadRequest, "作品分组查询参数无效")
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
			if err != nil || limit < 1 || limit > localDouyinExecutionRoundBatch {
				websession.Fail(w, http.StatusBadRequest, "每页作品分组数须为 1–1000")
				return
			}
		}
		cursor := query.Get("cursor")
		if _, explicit := query["cursor"]; explicit && (cursor == "" || len(cursor) > 256) {
			websession.Fail(w, http.StatusBadRequest, "作品分组续页位置无效")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := current(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		result, err := m.listExecutionWorkGroups(ctx, scope, day, limit, cursor)
		if err != nil {
			if errors.Is(err, errLocalDouyinExecutionWorksCursor) {
				websession.Fail(w, http.StatusBadRequest, "作品分组续页位置无效")
			} else {
				websession.Fail(w, http.StatusServiceUnavailable, "执行回合账本读取失败")
			}
			return
		}
		if err := current(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		websession.Respond(w, http.StatusOK, result)
	}))
}

func (m *localDouyinWebLogin) executionRoundReadScope(owner *websession.Store, w http.ResponseWriter, r *http.Request) (string, func() error, bool) {
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || !fishidentity.ValidFishID(identity.FishID) {
		websession.Fail(w, http.StatusConflict, "请先准备本机主人身份")
		return "", nil, false
	}
	session, _, ok := owner.Lookup(r)
	if !ok {
		websession.Fail(w, http.StatusUnauthorized, "请重新登录工作站")
		return "", nil, false
	}
	record, _, err := m.noticeState(identity.FishID)
	if err != nil || record.NoticeAccount == "" {
		websession.Fail(w, http.StatusForbidden, "请先绑定并启用当前抖音互动通知账号")
		return "", nil, false
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
	return localDouyinExecutionScope(identity.FishID, identity.LocalOwner.ID, record), current, true
}

func (m *localDouyinWebLogin) listExecutionRounds(ctx context.Context, scope, day string, before int64, limit int) (localDouyinExecutionRoundsResult, error) {
	return m.listExecutionRoundsForWork(ctx, scope, day, before, limit, nil)
}

func (m *localDouyinWebLogin) listExecutionRoundsForWork(ctx context.Context, scope, day string, before int64, limit int, workFilter *string) (localDouyinExecutionRoundsResult, error) {
	result := localDouyinExecutionRoundsResult{Date: day, Rounds: []localDouyinExecutionRound{}, EffectiveLimit: limit, Coverage: "notice_metadata"}
	if workFilter != nil {
		result.GroupSelected = true
		result.SelectedWorkID = *workFilter
	}
	db, err := openLocalDouyinExecutionDB(ctx, m.home, false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer db.Close()
	var rows *sql.Rows
	if workFilter != nil && before == 0 {
		rows, err = db.QueryContext(ctx, `SELECT sequence,notice_id,type,work_id,created_at,observed_at FROM rounds WHERE scope=? AND observed_date=? AND work_id=? ORDER BY sequence DESC LIMIT ?`, scope, day, *workFilter, limit+1)
	} else if workFilter != nil {
		rows, err = db.QueryContext(ctx, `SELECT sequence,notice_id,type,work_id,created_at,observed_at FROM rounds WHERE scope=? AND observed_date=? AND work_id=? AND sequence<? ORDER BY sequence DESC LIMIT ?`, scope, day, *workFilter, before, limit+1)
	} else if before == 0 {
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
		row.DetailStatus = "not_observed"
		row.DetailCandidates = []stationbrowser.DouyinNoticeCandidate{}
		result.Rounds = append(result.Rounds, row)
		last = sequence
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if err := readLocalDouyinRoundCandidates(ctx, db, scope, result.Rounds); err != nil {
		return result, err
	}
	return result, readLocalDouyinRoundDetails(ctx, db, scope, result.Rounds)
}

func (m *localDouyinWebLogin) listExecutionWorkGroups(ctx context.Context, scope, day string, limit int, cursor string) (localDouyinExecutionWorkGroupsResult, error) {
	result := localDouyinExecutionWorkGroupsResult{Date: day, Groups: []localDouyinExecutionWorkGroup{}, EffectiveLimit: limit, Coverage: "notice_work_groups"}
	position := localDouyinExecutionWorksCursor{Date: day, Scope: localCheckpointKey("douyin-execution-works-cursor", scope)}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 192 || base64.RawURLEncoding.EncodeToString(raw) != cursor || strictLocalMemoryDecode(raw, &position) != nil ||
			position.Anchor < 1 || position.Before < 1 || position.Before > position.Anchor || position.Date != day || position.Scope != localCheckpointKey("douyin-execution-works-cursor", scope) {
			return result, errLocalDouyinExecutionWorksCursor
		}
	}
	db, err := openLocalDouyinExecutionDB(ctx, m.home, false)
	if errors.Is(err, os.ErrNotExist) {
		if cursor != "" {
			return result, errLocalDouyinExecutionWorksCursor
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer db.Close()
	if cursor == "" {
		if err := db.QueryRowContext(ctx, `SELECT coalesce(max(sequence),0) FROM rounds WHERE scope=? AND observed_date=?`, scope, day).Scan(&position.Anchor); err != nil {
			return result, err
		}
		if position.Anchor == 0 {
			return result, nil
		}
	}
	var rows *sql.Rows
	if position.Before == 0 {
		rows, err = db.QueryContext(ctx, `SELECT work_id,count(*),max(sequence) FROM rounds WHERE scope=? AND observed_date=? AND sequence<=? GROUP BY work_id ORDER BY max(sequence) DESC LIMIT ?`,
			scope, day, position.Anchor, limit+1)
	} else {
		rows, err = db.QueryContext(ctx, `SELECT work_id,count(*),max(sequence) FROM rounds WHERE scope=? AND observed_date=? AND sequence<=? GROUP BY work_id HAVING max(sequence)<? ORDER BY max(sequence) DESC LIMIT ?`,
			scope, day, position.Anchor, position.Before, limit+1)
	}
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		group := localDouyinExecutionWorkGroup{Ownership: "unverified"}
		var latest int64
		if err := rows.Scan(&group.WorkID, &group.NoticeCount, &latest); err != nil {
			return result, err
		}
		if len(result.Groups) == limit {
			position.Before = last
			raw, err := json.Marshal(position)
			if err != nil {
				return result, err
			}
			result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		result.Groups = append(result.Groups, group)
		last = latest
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, rows.Close()
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

func readLocalDouyinRoundDetails(ctx context.Context, db *sql.DB, scope string, rounds []localDouyinExecutionRound) error {
	if len(rounds) == 0 {
		return nil
	}
	// A read of a ledger created before detail capture remains read-only.
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='round_details'`).Scan(&tableCount); err != nil || tableCount == 0 {
		return err
	}
	indices := make(map[string]int, len(rounds))
	for index, round := range rounds {
		indices[round.NoticeID] = index
	}
	for start := 0; start < len(rounds); start += 200 {
		end := min(start+200, len(rounds))
		placeholders := make([]string, end-start)
		args := make([]any, 0, end-start+1)
		args = append(args, scope)
		for index, round := range rounds[start:end] {
			placeholders[index] = "?"
			args = append(args, round.NoticeID)
		}
		suffix := ` WHERE d.scope=? AND d.notice_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY d.notice_id`
		query := `SELECT d.notice_id,d.work_id,d.status,coalesce(c.creator_account,'') FROM round_details d
			LEFT JOIN round_creator_accounts c ON c.scope=d.scope AND c.notice_id=d.notice_id` + suffix
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var noticeID, workID, status, creatorAccount string
			if err := rows.Scan(&noticeID, &workID, &status, &creatorAccount); err != nil {
				rows.Close()
				return err
			}
			index, found := indices[noticeID]
			if !found || workID != rounds[index].WorkID || !validDouyinRoundDetailStatus(status) ||
				(status == "observed_candidate" || status == "observed_no_candidates") && !validDouyinNoticeAccount(creatorAccount) {
				rows.Close()
				return errors.New("抖音执行回合详情证据无效")
			}
			rounds[index].DetailStatus = status
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		query = `SELECT notice_id,path,candidate_id FROM round_detail_candidates WHERE scope=? AND notice_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY notice_id,path`
		rows, err = db.QueryContext(ctx, query, args...)
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
			if !found || rounds[index].DetailStatus != "observed_candidate" || !validDouyinRoundCandidate(candidate) || len(rounds[index].DetailCandidates) >= 16 {
				rows.Close()
				return errors.New("抖音执行回合详情候选字段无效")
			}
			rounds[index].DetailCandidates = append(rounds[index].DetailCandidates, candidate)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, round := range rounds[start:end] {
			if round.DetailStatus == "observed_candidate" && len(round.DetailCandidates) == 0 {
				return errors.New("抖音执行回合详情候选字段缺失")
			}
		}
	}
	return nil
}
