package station

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const localDouyinAutoCommentLikesPath = "/api/local/apps/douyin/auto-comment-likes"

// This ledger is deliberately separate from video likes. Only a durable
// submit intent blocks later attempts; pre-submit observation can be retried.
const douyinAutoCommentLikesSchema = `CREATE TABLE IF NOT EXISTS comment_likes (
 owner TEXT NOT NULL, profile TEXT NOT NULL, grant TEXT NOT NULL,
 account TEXT NOT NULL, work_id TEXT NOT NULL, comment_id TEXT NOT NULL,
 author_id TEXT NOT NULL, text_hash TEXT NOT NULL,
 state TEXT NOT NULL, code TEXT NOT NULL, submitted INTEGER NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(owner,profile,grant,account,work_id,comment_id)
);
CREATE TABLE IF NOT EXISTS scan_progress (
 owner TEXT NOT NULL, profile TEXT NOT NULL, grant TEXT NOT NULL,
 account TEXT NOT NULL, work_id TEXT NOT NULL, cursor TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(owner,profile,grant,account)
);
CREATE TABLE IF NOT EXISTS auto_seen (
 owner TEXT NOT NULL, profile TEXT NOT NULL, grant TEXT NOT NULL,
 account TEXT NOT NULL, epoch INTEGER NOT NULL, work_id TEXT NOT NULL,
 comment_id TEXT NOT NULL,
 PRIMARY KEY(owner,profile,grant,account,epoch,work_id,comment_id)
);
CREATE TABLE IF NOT EXISTS comment_like_jobs (
 owner TEXT NOT NULL, profile TEXT NOT NULL, grant TEXT NOT NULL,
 account TEXT NOT NULL, mode TEXT NOT NULL, epoch INTEGER NOT NULL,
 catalog_hash TEXT NOT NULL, state TEXT NOT NULL, work_id TEXT NOT NULL,
 cursor TEXT NOT NULL, root_ids TEXT NOT NULL, root_index INTEGER NOT NULL,
 reply_cursor TEXT NOT NULL, next_cursor TEXT NOT NULL, main_complete INTEGER NOT NULL,
 completed_works INTEGER NOT NULL, seen INTEGER NOT NULL, submitted INTEGER NOT NULL,
 unverified INTEGER NOT NULL, last_error TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(owner,profile,grant,account,mode)
);`

type localDouyinAutoCommentLikeInput struct {
	Action        string `json:"action"`
	Revision      uint64 `json:"revision"`
	WorkID        string `json:"work_id"`
	Cursor        string `json:"cursor,omitempty"`
	RootCommentID string `json:"root_comment_id,omitempty"`
}

type localDouyinAutoCommentLikeCounts struct {
	Applied int `json:"applied"`
	Unknown int `json:"unknown"`
	Failed  int `json:"failed"`
}

type localDouyinAutoCommentLikeUncovered struct {
	WorkID string `json:"work_id"`
	Reason string `json:"reason"`
}

func douyinAutoCommentLikeWorkReason(work localDouyinCatalogWork) string {
	if work.Visibility != "公开" {
		return "非公开作品的评论页面尚未核实"
	}
	if work.ReviewStatus != "not_reviewing" {
		return "作品仍在审核或审核状态未知"
	}
	if work.MediaType != "video" {
		return "图文或未知类型作品的评论页面尚未核实"
	}
	return ""
}

type localDouyinAutoCommentLikeStatus struct {
	Enabled        bool                                        `json:"enabled"`
	Revision       uint64                                      `json:"revision"`
	CatalogState   string                                      `json:"catalog_state"`
	CoverageState  string                                      `json:"coverage_state"`
	Reason         string                                      `json:"reason"`
	Works          map[string]localDouyinAutoCommentLikeCounts `json:"works"`
	WorkID         string                                      `json:"work_id,omitempty"`
	NextCursor     string                                      `json:"next_cursor,omitempty"`
	MainListEnd    bool                                        `json:"main_list_end,omitempty"`
	Seen           int                                         `json:"seen,omitempty"`
	Skipped        int                                         `json:"skipped,omitempty"`
	Submitted      int                                         `json:"submitted,omitempty"`
	Unverified     int                                         `json:"unverified,omitempty"`
	ScanWorkID     string                                      `json:"scan_work_id,omitempty"`
	ScanCursor     string                                      `json:"scan_cursor,omitempty"`
	EligibleWorks  int                                         `json:"eligible_works"`
	Uncovered      []localDouyinAutoCommentLikeUncovered       `json:"uncovered_works"`
	Auto           localDouyinLikeAutoStatus                   `json:"auto"`
	Batch          localDouyinLikeBatchStatus                  `json:"batch"`
	RootCommentID  string                                      `json:"root_comment_id,omitempty"`
	PageEnd        bool                                        `json:"page_end,omitempty"`
	PageCommentIDs []string                                    `json:"-"`
}

func douyinAutoCommentLikesPath(home *localHomeService, fish string) string {
	return filepath.Join(stationApplicationsDir(home.dataDir), "douyin", "web-login", fish, "auto-comment-likes.sqlite")
}

func openDouyinAutoCommentLikes(ctx context.Context, home *localHomeService, fish string, create bool) (*sql.DB, error) {
	path := douyinAutoCommentLikesPath(home, fish)
	open := workspacefs.OpenPath
	if create {
		open = workspacefs.EnsurePath
	}
	root, err := open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if err != nil && (!create || !errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	if info != nil && !info.Mode().IsRegular() {
		return nil, errors.New("点赞记录路径不是普通文件")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err == nil && create {
		_, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL")
	}
	if err == nil {
		_, err = db.ExecContext(ctx, douyinAutoCommentLikesSchema)
	}
	if err == nil && create {
		err = os.Chmod(path, 0600)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func localDouyinAutoCommentLikeReady(r localDouyinWebRecord, v localDouyinWebView) bool {
	return r.AutoCommentLikesEnabled && v.AutoCommentLikesEnabled && localDouyinCommentLikeReady(r, v)
}

func localDouyinCommentLikeReady(r localDouyinWebRecord, v localDouyinWebView) bool {
	return v.State == "saved" && v.Available &&
		r.ReadEnabled && r.WorksEnabled && r.CommentsEnabled && r.LikesEnabled && v.LikesEnabled &&
		localDouyinWebScope(r) == "home_and_videos"
}

func (m *localDouyinWebLogin) autoCommentLikeCatalog(ctx context.Context, fish, owner string) (localDouyinCatalogResponse, localDouyinWebRecord, string, error) {
	return m.commentLikeCatalog(ctx, fish, owner, true)
}

func (m *localDouyinWebLogin) commentLikeCatalog(ctx context.Context, fish, owner string, requireAuto bool) (localDouyinCatalogResponse, localDouyinWebRecord, string, error) {
	catalog, record, err := m.readWorksCatalog(ctx, fish, owner)
	if err != nil {
		return catalog, record, "本人作品目录不可读", err
	}
	m.mu.Lock()
	view := m.view(record)
	m.mu.Unlock()
	if !record.LikesEnabled || !view.LikesEnabled {
		return catalog, record, "请先开启独立的允许点赞授权", errLocalDouyinNoticeGrant
	}
	if !localDouyinCommentLikeReady(record, view) || requireAuto && !localDouyinAutoCommentLikeReady(record, view) {
		return catalog, record, "评论点赞所需网页读取授权尚未启用", errLocalDouyinNoticeGrant
	}
	if catalog.Coverage.State != "complete" || catalog.Coverage.PreviousComplete ||
		catalog.Coverage.HasMore == nil || *catalog.Coverage.HasMore || catalog.AccountID == "" {
		return catalog, record, "本人作品目录尚未完整核对", errLocalDouyinNoticeGrant
	}
	return catalog, record, "", nil
}

func (m *localDouyinWebLogin) autoCommentLikeStatus(ctx context.Context, fish, owner string) (localDouyinAutoCommentLikeStatus, error) {
	status := localDouyinAutoCommentLikeStatus{CatalogState: "unknown", CoverageState: "unsupported", Works: map[string]localDouyinAutoCommentLikeCounts{},
		Uncovered: []localDouyinAutoCommentLikeUncovered{}, Reason: "仅覆盖已核对的公开视频作品评论；其他作品及未能完整读取的回复不计为完成",
		Auto: localDouyinLikeAutoStatus{State: "off"}, Batch: localDouyinLikeBatchStatus{State: "idle", DelayMinMS: 1800, DelayMaxMS: 4200}}
	catalog, record, reason, err := m.commentLikeCatalog(ctx, fish, owner, false)
	status.Revision, status.CatalogState = record.Revision, catalog.Coverage.State
	status.Enabled = record.AutoCommentLikesEnabled
	if status.Enabled {
		status.Auto.State = "arming"
	}
	if err != nil {
		status.Reason = reason
		if status.Enabled {
			status.Auto.LastError = reason
		}
		return status, nil
	}
	status.CoverageState = "partial"
	for _, work := range catalog.Works {
		if reason := douyinAutoCommentLikeWorkReason(work); reason != "" {
			status.Uncovered = append(status.Uncovered, localDouyinAutoCommentLikeUncovered{WorkID: work.ID, Reason: reason})
		} else {
			status.EligibleWorks++
		}
	}
	status.Auto.BaselineTotalWorks = status.EligibleWorks
	status.Batch.TotalWorks = status.EligibleWorks
	if status.Enabled {
		status.Auto.State = "arming"
	}
	db, err := openDouyinAutoCommentLikes(ctx, m.home, fish, false)
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	defer db.Close()
	a := (&localDouyinWebObservation{home: m.home}).access(fish, owner, record, localDouyinWebView{})
	rows, err := db.QueryContext(ctx, `SELECT work_id,state,COUNT(*) FROM comment_likes
WHERE owner=? AND profile=? AND grant=? AND account=? GROUP BY work_id,state`, owner,
		a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID)
	if err != nil {
		return status, err
	}
	defer rows.Close()
	for rows.Next() {
		var work, state string
		var count int
		if rows.Scan(&work, &state, &count) != nil {
			return status, errors.New("点赞状态读取失败")
		}
		counts := status.Works[work]
		switch state {
		case "applied":
			counts.Applied += count
		case "failed":
			counts.Failed += count
		default:
			counts.Unknown += count
		}
		status.Works[work] = counts
	}
	if err := rows.Err(); err != nil {
		return status, err
	}
	if err := rows.Close(); err != nil {
		return status, err
	}
	if err := m.commentLikeJobStatuses(ctx, db, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID, record, catalog, &status); err != nil {
		return status, err
	}
	return status, nil
}

func (m *localDouyinWebLogin) autoCommentLikeRun(ctx context.Context, fish, owner string, input localDouyinAutoCommentLikeInput) (localDouyinAutoCommentLikeStatus, int, error) {
	requireAuto := input.Action == "auto" || input.Action == "baseline"
	status, err := m.autoCommentLikeStatus(ctx, fish, owner)
	if err != nil {
		return status, http.StatusServiceUnavailable, err
	}
	if input.Revision != status.Revision {
		return status, http.StatusConflict, errors.New("网页登录状态已变化，请刷新后执行")
	}
	if requireAuto && !status.Enabled {
		return status, http.StatusConflict, errors.New("自动点赞已关闭")
	}
	if !validLocalDouyinCommentID(input.WorkID) || input.Cursor != "" && !validLocalDouyinCommentID(input.Cursor) ||
		input.RootCommentID != "" && !validLocalDouyinCommentID(input.RootCommentID) {
		return status, http.StatusBadRequest, errors.New("作品或评论续页编号无效")
	}
	catalog, record, _, err := m.commentLikeCatalog(ctx, fish, owner, requireAuto)
	if err != nil || record.Revision != input.Revision {
		return status, http.StatusConflict, errors.New("本人作品目录或授权已变化")
	}
	member := false
	for _, work := range catalog.Works {
		if work.ID == input.WorkID && douyinAutoCommentLikeWorkReason(work) == "" {
			member = true
			break
		}
	}
	if !member {
		return status, http.StatusForbidden, errors.New("作品未在完整目录中核实为当前公开作品")
	}
	profile := filepath.Join(filepath.Dir(douyinWorksCatalogPath(m.home, fish)), record.Profile)
	a := (&localDouyinWebObservation{home: m.home}).access(fish, owner, record, localDouyinWebView{})
	var db *sql.DB
	authorize := func() error {
		current, saved, _, err := m.commentLikeCatalog(ctx, fish, owner, requireAuto)
		if err != nil || saved != record || current.AccountID != catalog.AccountID ||
			!m.worksCatalogStillAuthorized(fish, owner, record, catalog.AccountID) {
			return errLocalDouyinNoticeGrant
		}
		if input.Action == "batch" && db != nil {
			running, err := commentLikeBatchIsRunning(ctx, db, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID)
			if err != nil || !running {
				return errLocalDouyinLikeCancelled
			}
		}
		return nil
	}
	m.mu.Lock()
	readWorks, readComments, like := m.readWorks, m.readComments, m.commentLike
	m.mu.Unlock()
	if readWorks == nil || readComments == nil || like == nil {
		return status, http.StatusServiceUnavailable, errors.New("评论点赞执行器尚未就绪")
	}
	// Recheck the creator account in the current browser profile. The saved
	// catalog alone cannot prove that the browser has not switched accounts.
	if err := authorize(); err != nil {
		return status, http.StatusConflict, err
	}
	proof, err := readWorks(ctx, profile, stationbrowser.DouyinCreatorWorksRequest{}, authorize)
	if err != nil || proof.Account == "" || proof.PageKey == "" || proof.Account != catalog.AccountID || !creatorOwnerCurrent(a, proof.Account) {
		return status, http.StatusConflict, errors.New("当前创作者账号与已核对的本人作品目录不一致")
	}
	if err := authorize(); err != nil {
		return status, http.StatusConflict, err
	}
	page, err := readComments(ctx, profile, stationbrowser.DouyinCommentsRequest{VideoID: input.WorkID, RootCommentID: input.RootCommentID, Cursor: input.Cursor, Limit: 20}, authorize)
	if err != nil {
		return status, http.StatusServiceUnavailable, errors.New("当前作品评论无法安全读取")
	}
	if len(page.Comments) > 20 || !page.Complete && (len(page.Comments) == 0 || page.NextCursor != page.Comments[len(page.Comments)-1].ID) ||
		page.Complete && page.NextCursor != "" {
		return status, http.StatusServiceUnavailable, errors.New("评论分页证据无效")
	}
	seen := map[string]bool{input.Cursor: true}
	for _, comment := range page.Comments {
		if !validLocalDouyinCommentID(comment.ID) || seen[comment.ID] || strings.TrimSpace(comment.Text) == "" ||
			comment.RootCommentID != input.RootCommentID || strings.TrimSpace(comment.AuthorID) == "" ||
			len(comment.AuthorID) > 256 || strings.ContainsAny(comment.AuthorID, "\x00\r\n") {
			return status, http.StatusServiceUnavailable, errors.New("评论身份或内容证据无效")
		}
		seen[comment.ID] = true
		status.PageCommentIDs = append(status.PageCommentIDs, comment.ID)
	}
	db, err = openDouyinAutoCommentLikes(ctx, m.home, fish, true)
	if err != nil {
		return status, http.StatusServiceUnavailable, errors.New("点赞记录无法持久保存")
	}
	defer db.Close()
	status.WorkID, status.RootCommentID, status.PageEnd, status.NextCursor = input.WorkID, input.RootCommentID, page.Complete, page.NextCursor
	status.MainListEnd = input.RootCommentID == "" && page.Complete
	for _, comment := range page.Comments {
		if err := authorize(); err != nil {
			return status, http.StatusConflict, err
		}
		status.Seen++
		if input.Action == "baseline" {
			if err := insertDouyinAutoSeen(ctx, db, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID, record.Revision, input.WorkID, comment.ID); err != nil {
				return status, http.StatusServiceUnavailable, errors.New("自动点赞基线无法持久保存")
			}
			status.Skipped++
			continue
		}
		if input.Action == "auto" {
			seen, err := douyinAutoSeen(ctx, db, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID, record.Revision, input.WorkID, comment.ID)
			if err != nil {
				return status, http.StatusServiceUnavailable, errors.New("自动点赞基线无法核对")
			}
			if seen {
				status.Skipped++
				continue
			}
		}
		hash := sha256.Sum256([]byte(comment.Text))
		var priorState string
		var priorSubmitted int
		err := db.QueryRowContext(ctx, `SELECT state,submitted FROM comment_likes
WHERE owner=? AND profile=? AND grant=? AND account=? AND work_id=? AND comment_id=?`,
			owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID,
			input.WorkID, comment.ID).Scan(&priorState, &priorSubmitted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return status, http.StatusServiceUnavailable, errors.New("点赞记录无法核对")
		}
		if err == nil && (priorSubmitted != 0 || priorState == "applied") {
			status.Skipped++
			if input.Action == "auto" {
				if err := insertDouyinAutoSeen(ctx, db, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID, record.Revision, input.WorkID, comment.ID); err != nil {
					return status, http.StatusServiceUnavailable, errors.New("自动点赞进度无法保存")
				}
			}
			continue
		}
		if err == nil {
			if _, err := db.ExecContext(ctx, `DELETE FROM comment_likes WHERE owner=? AND profile=? AND grant=? AND account=? AND work_id=? AND comment_id=? AND submitted=0`,
				owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID, input.WorkID, comment.ID); err != nil {
				return status, http.StatusServiceUnavailable, errors.New("旧点赞记录无法核对")
			}
		}
		intentInserted := false
		before := func(context.Context) error {
			if err := authorize(); err != nil {
				return err
			}
			result, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO comment_likes
(owner,profile,grant,account,work_id,comment_id,author_id,text_hash,state,code,submitted,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID,
				input.WorkID, comment.ID, comment.AuthorID, hex.EncodeToString(hash[:]), "unknown", "submitted", 1, time.Now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
			inserted, err := result.RowsAffected()
			if err != nil || inserted != 1 {
				return errors.New("该评论已有点赞提交意图")
			}
			intentInserted = true
			return nil
		}
		if input.Action == "batch" || input.Action == "auto" {
			m.mu.Lock()
			pause := m.commentLikePause
			m.mu.Unlock()
			if pause == nil {
				pause = randomDouyinCommentLikePause
			}
			if err := pause(ctx); err != nil {
				return status, http.StatusConflict, err
			}
		}
		outcome, runErr := like(ctx, profile, stationbrowser.DouyinCommentLikeRequest{VideoID: input.WorkID,
			CommentID: comment.ID, RootCommentID: comment.RootCommentID, ExpectedText: comment.Text, ExpectedAuthorID: comment.AuthorID,
			BeforeSubmit: before}, authorize)
		if !intentInserted && !(runErr == nil && outcome.State == "applied") {
			// No request crossed the durable submit boundary. Keep a retryable
			// failure and the page cursor; a later run can re-observe this comment.
			code := outcome.Code
			if code == "" {
				code = "pre_submit_unverified"
			}
			if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO comment_likes
(owner,profile,grant,account,work_id,comment_id,author_id,text_hash,state,code,submitted,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID,
				input.WorkID, comment.ID, comment.AuthorID, hex.EncodeToString(hash[:]), "failed", code, 0, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return status, http.StatusServiceUnavailable, errors.New("未提交的点赞失败记录无法保存")
			}
			status.Unverified++
			if runErr != nil && errors.Is(runErr, errLocalDouyinLikeCancelled) {
				return status, http.StatusConflict, runErr
			}
			if runErr != nil && errors.Is(runErr, errLocalDouyinNoticeGrant) {
				return status, http.StatusConflict, runErr
			}
			return status, http.StatusServiceUnavailable, errors.New("评论点赞提交前未通过核对，当前页保留待重试")
		}
		if input.Action == "auto" {
			if err := insertDouyinAutoSeen(ctx, db, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID, record.Revision, input.WorkID, comment.ID); err != nil {
				return status, http.StatusServiceUnavailable, errors.New("自动点赞进度无法保存")
			}
		}
		state := "unknown"
		if runErr == nil && outcome.State == "applied" {
			state = "applied"
		}
		code := outcome.Code
		if code == "" {
			code = "execution_unknown"
		}
		if !intentInserted {
			if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO comment_likes
(owner,profile,grant,account,work_id,comment_id,author_id,text_hash,state,code,submitted,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, owner, a.connection.AccountID, douyinWorksCatalogGrant(record), catalog.AccountID,
				input.WorkID, comment.ID, comment.AuthorID, hex.EncodeToString(hash[:]), state, code, 0, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return status, http.StatusServiceUnavailable, errors.New("已点赞结果无法保存")
			}
			continue
		}
		status.Submitted++
		if _, err := db.ExecContext(ctx, `UPDATE comment_likes SET state=?,code=?,updated_at=?
WHERE owner=? AND profile=? AND grant=? AND account=? AND work_id=? AND comment_id=?`,
			state, code, time.Now().UTC().Format(time.RFC3339Nano), owner, a.connection.AccountID,
			douyinWorksCatalogGrant(record), catalog.AccountID, input.WorkID, comment.ID); err != nil {
			return status, http.StatusServiceUnavailable, errors.New("点赞结果无法保存；本条状态未知，不再自动重试")
		}
		if runErr != nil && errors.Is(runErr, errLocalDouyinNoticeGrant) {
			return status, http.StatusConflict, runErr
		}
	}
	updated, err := m.autoCommentLikeStatus(ctx, fish, owner)
	if err != nil {
		return status, http.StatusServiceUnavailable, err
	}
	updated.WorkID, updated.RootCommentID, updated.MainListEnd, updated.PageEnd, updated.NextCursor = status.WorkID, status.RootCommentID, status.MainListEnd, status.PageEnd, status.NextCursor
	updated.Seen, updated.Skipped, updated.Submitted, updated.Unverified = status.Seen, status.Skipped, status.Submitted, status.Unverified
	updated.PageCommentIDs = status.PageCommentIDs
	return updated, http.StatusOK, nil
}

func (m *localDouyinWebLogin) autoCommentLikeRoutes(owner *websession.Store, mux *http.ServeMux) {
	handler := owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		identity, err := m.home.identity.Read()
		if err != nil || identity.LocalOwner == nil || !fishidentity.ValidFishID(identity.FishID) {
			websession.Fail(w, http.StatusConflict, "请先准备本机主人身份")
			return
		}
		if r.Method == http.MethodGet {
			status, err := m.autoCommentLikeStatus(r.Context(), identity.FishID, identity.LocalOwner.ID)
			if err != nil {
				websession.Fail(w, http.StatusServiceUnavailable, "点赞状态暂不可读")
				return
			}
			websession.Respond(w, http.StatusOK, status)
			return
		}
		var input localDouyinAutoCommentLikeInput
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if !websession.Decode(w, r, &input) {
			return
		}
		var status localDouyinAutoCommentLikeStatus
		var code int
		switch input.Action {
		case "run":
			status, code, err = m.autoCommentLikeRun(r.Context(), identity.FishID, identity.LocalOwner.ID, input)
		case "start_all", "run_all", "resume_all":
			status, code, err = m.commentLikeBatchSet(r.Context(), identity.FishID, identity.LocalOwner.ID, true)
			if err == nil {
				go func() {
					runCtx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
					defer cancel()
					_, _ = m.commentLikeStep(runCtx, "batch")
				}()
			}
		case "cancel_all":
			status, code, err = m.commentLikeBatchSet(r.Context(), identity.FishID, identity.LocalOwner.ID, false)
		default:
			websession.Fail(w, http.StatusBadRequest, "请指定点赞动作")
			return
		}
		if err != nil {
			websession.Fail(w, code, err.Error())
			return
		}
		websession.Respond(w, http.StatusOK, status)
	})
	mux.HandleFunc("GET "+localDouyinAutoCommentLikesPath, handler)
	mux.HandleFunc("POST "+localDouyinAutoCommentLikesPath, handler)
}
