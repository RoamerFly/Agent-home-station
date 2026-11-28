package station

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wechat-bot/internal/hostfile"
)

var errLocalDouyinLikeCancelled = errors.New("评论点赞批量任务已取消")

type localDouyinLikeAutoStatus struct {
	State                  string `json:"state"`
	BaselineCompletedWorks int    `json:"baseline_completed_works"`
	BaselineTotalWorks     int    `json:"baseline_total_works"`
	LastError              string `json:"last_error,omitempty"`
}

type localDouyinLikeBatchStatus struct {
	State          string `json:"state"`
	WorkID         string `json:"work_id,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
	RootCommentID  string `json:"root_comment_id,omitempty"`
	Seen           int    `json:"seen"`
	Submitted      int    `json:"submitted"`
	Unverified     int    `json:"unverified"`
	CompletedWorks int    `json:"completed_works"`
	TotalWorks     int    `json:"total_works"`
	LastError      string `json:"last_error,omitempty"`
	DelayMinMS     int    `json:"delay_min_ms"`
	DelayMaxMS     int    `json:"delay_max_ms"`
}

type douyinCommentLikeJob struct {
	Mode, CatalogHash, State, WorkID, Cursor string
	RootIDs                                  []string
	RootIndex                                int
	ReplyCursor, NextCursor                  string
	MainComplete                             bool
	CompletedWorks, Seen, Submitted          int
	Unverified                               int
	LastError                                string
	Epoch                                    uint64
}

func commentLikeCatalogWorks(catalog localDouyinCatalogResponse) []string {
	works := make([]string, 0, len(catalog.Works))
	for _, work := range catalog.Works {
		if douyinAutoCommentLikeWorkReason(work) == "" {
			works = append(works, work.ID)
		}
	}
	sort.Strings(works)
	return works
}

func commentLikeCatalogHash(works []string) string {
	h := sha256.Sum256([]byte(strings.Join(works, "\n")))
	return hex.EncodeToString(h[:])
}

func loadDouyinCommentLikeJob(ctx context.Context, db *sql.DB, owner, profile, grant, account, mode string) (douyinCommentLikeJob, bool, error) {
	var job douyinCommentLikeJob
	var epoch int64
	var rootIDs string
	var mainComplete int
	err := db.QueryRowContext(ctx, `SELECT epoch,catalog_hash,state,work_id,cursor,root_ids,root_index,
reply_cursor,next_cursor,main_complete,completed_works,seen,submitted,unverified,last_error
FROM comment_like_jobs WHERE owner=? AND profile=? AND grant=? AND account=? AND mode=?`,
		owner, profile, grant, account, mode).Scan(&epoch, &job.CatalogHash, &job.State, &job.WorkID,
		&job.Cursor, &rootIDs, &job.RootIndex, &job.ReplyCursor, &job.NextCursor, &mainComplete,
		&job.CompletedWorks, &job.Seen, &job.Submitted, &job.Unverified, &job.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return job, false, nil
	}
	if err != nil {
		return job, false, err
	}
	if epoch < 0 || json.Unmarshal([]byte(rootIDs), &job.RootIDs) != nil {
		return job, false, errors.New("评论点赞续办位置损坏")
	}
	job.Mode, job.Epoch, job.MainComplete = mode, uint64(epoch), mainComplete != 0
	return job, true, nil
}

func saveDouyinCommentLikeJob(ctx context.Context, db *sql.DB, owner, profile, grant, account string, job douyinCommentLikeJob) error {
	rootIDs, err := json.Marshal(job.RootIDs)
	if err != nil {
		return err
	}
	mainComplete := 0
	if job.MainComplete {
		mainComplete = 1
	}
	_, err = db.ExecContext(ctx, `INSERT INTO comment_like_jobs
(owner,profile,grant,account,mode,epoch,catalog_hash,state,work_id,cursor,root_ids,root_index,
reply_cursor,next_cursor,main_complete,completed_works,seen,submitted,unverified,last_error,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(owner,profile,grant,account,mode) DO UPDATE SET
epoch=excluded.epoch,catalog_hash=excluded.catalog_hash,state=excluded.state,
work_id=excluded.work_id,cursor=excluded.cursor,root_ids=excluded.root_ids,
root_index=excluded.root_index,reply_cursor=excluded.reply_cursor,
next_cursor=excluded.next_cursor,main_complete=excluded.main_complete,
completed_works=excluded.completed_works,seen=excluded.seen,submitted=excluded.submitted,
unverified=excluded.unverified,last_error=excluded.last_error,updated_at=excluded.updated_at`,
		owner, profile, grant, account, job.Mode, int64(job.Epoch), job.CatalogHash, job.State,
		job.WorkID, job.Cursor, string(rootIDs), job.RootIndex, job.ReplyCursor, job.NextCursor,
		mainComplete, job.CompletedWorks, job.Seen, job.Submitted, job.Unverified,
		job.LastError, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func douyinAutoSeen(ctx context.Context, db *sql.DB, owner, profile, grant, account string, epoch uint64, work, comment string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM auto_seen WHERE owner=? AND profile=? AND grant=? AND account=?
AND epoch=? AND work_id=? AND comment_id=?`, owner, profile, grant, account, int64(epoch), work, comment).Scan(&one)
	return err == nil, firstNonNoRows(err)
}

func firstNonNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func insertDouyinAutoSeen(ctx context.Context, db *sql.DB, owner, profile, grant, account string, epoch uint64, work, comment string) error {
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO auto_seen
(owner,profile,grant,account,epoch,work_id,comment_id) VALUES(?,?,?,?,?,?,?)`,
		owner, profile, grant, account, int64(epoch), work, comment)
	return err
}

func commentLikeBatchIsRunning(ctx context.Context, db *sql.DB, owner, profile, grant, account string) (bool, error) {
	var state string
	err := db.QueryRowContext(ctx, `SELECT state FROM comment_like_jobs
WHERE owner=? AND profile=? AND grant=? AND account=? AND mode='batch'`,
		owner, profile, grant, account).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return state == "running", err
}

func (m *localDouyinWebLogin) commentLikeJobStatuses(ctx context.Context, db *sql.DB, owner, profile, grant, account string,
	record localDouyinWebRecord, catalog localDouyinCatalogResponse, status *localDouyinAutoCommentLikeStatus) error {
	works := commentLikeCatalogWorks(catalog)
	hash := commentLikeCatalogHash(works)
	status.Auto.BaselineTotalWorks = len(works)
	status.Batch.TotalWorks = len(works)
	if status.Enabled {
		status.Auto.State = "arming"
	}
	auto, found, err := loadDouyinCommentLikeJob(ctx, db, owner, profile, grant, account, "auto")
	if err != nil {
		return err
	}
	if status.Enabled && found && auto.Epoch == record.Revision && auto.CatalogHash == hash {
		status.Auto.State = auto.State
		status.Auto.BaselineCompletedWorks = auto.CompletedWorks
		status.Auto.LastError = auto.LastError
		status.ScanWorkID, status.ScanCursor = auto.WorkID, auto.Cursor
	}
	batch, found, err := loadDouyinCommentLikeJob(ctx, db, owner, profile, grant, account, "batch")
	if err != nil {
		return err
	}
	if found {
		status.Batch.State = batch.State
		if batch.CatalogHash != hash && batch.State == "running" {
			status.Batch.State = "error"
			status.Batch.LastError = "本人作品目录已变化，点击继续将重新扫描当前目录"
		} else {
			status.Batch.LastError = batch.LastError
		}
		status.Batch.WorkID, status.Batch.Cursor = batch.WorkID, batch.Cursor
		if batch.RootIndex >= 0 && batch.RootIndex < len(batch.RootIDs) {
			status.Batch.RootCommentID = batch.RootIDs[batch.RootIndex]
			status.Batch.Cursor = batch.ReplyCursor
		}
		status.Batch.CompletedWorks, status.Batch.Seen, status.Batch.Submitted, status.Batch.Unverified =
			batch.CompletedWorks, batch.Seen, batch.Submitted, batch.Unverified
	}
	return nil
}

func (m *localDouyinWebLogin) commentLikeBatchSet(ctx context.Context, fish, owner string, start bool) (localDouyinAutoCommentLikeStatus, int, error) {
	status, err := m.autoCommentLikeStatus(ctx, fish, owner)
	if err != nil {
		return status, http.StatusServiceUnavailable, err
	}
	if !start {
		db, err := openDouyinAutoCommentLikes(ctx, m.home, fish, false)
		if errors.Is(err, os.ErrNotExist) {
			return status, http.StatusOK, nil
		}
		if err != nil {
			return status, http.StatusServiceUnavailable, err
		}
		defer db.Close()
		result, err := db.ExecContext(ctx, `UPDATE comment_like_jobs SET state='cancelled',updated_at=?
WHERE owner=? AND mode='batch' AND state='running'`, time.Now().UTC().Format(time.RFC3339Nano), owner)
		if err != nil {
			return status, http.StatusServiceUnavailable, err
		}
		status, err = m.autoCommentLikeStatus(ctx, fish, owner)
		if err != nil {
			return status, http.StatusServiceUnavailable, err
		}
		if affected, _ := result.RowsAffected(); affected > 0 && status.Batch.State == "idle" {
			status.Batch.State = "cancelled"
		}
		return status, http.StatusOK, nil
	}
	catalog, record, reason, err := m.commentLikeCatalog(ctx, fish, owner, false)
	if err != nil {
		return status, http.StatusConflict, errors.New(reason)
	}
	works := commentLikeCatalogWorks(catalog)
	if len(works) == 0 && start {
		return status, http.StatusConflict, errors.New("暂无可读取的公开视频作品评论")
	}
	db, err := openDouyinAutoCommentLikes(ctx, m.home, fish, true)
	if err != nil {
		return status, http.StatusServiceUnavailable, err
	}
	defer db.Close()
	a := (&localDouyinWebObservation{home: m.home}).access(fish, owner, record, localDouyinWebView{})
	profile, grant := a.connection.AccountID, douyinWorksCatalogGrant(record)
	job, found, err := loadDouyinCommentLikeJob(ctx, db, owner, profile, grant, catalog.AccountID, "batch")
	if err != nil {
		return status, http.StatusServiceUnavailable, err
	}
	hash := commentLikeCatalogHash(works)
	if !found || job.CatalogHash != hash || job.State == "complete" {
		job = douyinCommentLikeJob{Mode: "batch", CatalogHash: hash, State: "running", WorkID: works[0]}
	} else {
		job.State, job.LastError = "running", ""
	}
	if err := saveDouyinCommentLikeJob(ctx, db, owner, profile, grant, catalog.AccountID, job); err != nil {
		return status, http.StatusServiceUnavailable, err
	}
	status, err = m.autoCommentLikeStatus(ctx, fish, owner)
	if err != nil {
		return status, http.StatusServiceUnavailable, err
	}
	return status, http.StatusOK, nil
}

// The delay spaces requests and is cancellable. It does not fabricate
// identity, user gestures, or any platform receipt.
func randomDouyinCommentLikePause(ctx context.Context) error {
	const min, span = 1800, 2401 // inclusive 1.8–4.2 seconds
	n, err := rand.Int(rand.Reader, big.NewInt(span))
	ms := min + span/2
	if err == nil {
		ms = min + int(n.Int64())
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func resetDouyinCommentLikeJob(mode string, record localDouyinWebRecord, hash string, works []string) douyinCommentLikeJob {
	state := "running"
	if mode == "auto" {
		state = "arming"
	}
	job := douyinCommentLikeJob{Mode: mode, Epoch: record.Revision, CatalogHash: hash, State: state}
	if len(works) > 0 {
		job.WorkID = works[0]
	}
	return job
}

func advanceDouyinCommentLikeJob(job *douyinCommentLikeJob, works []string) error {
	if !job.MainComplete {
		if job.NextCursor == "" || job.NextCursor == job.Cursor {
			return errors.New("评论主列表续页位置未推进")
		}
		job.Cursor = job.NextCursor
		job.RootIDs, job.RootIndex, job.ReplyCursor, job.NextCursor, job.MainComplete = nil, 0, "", "", false
		return nil
	}
	index := -1
	for i, work := range works {
		if work == job.WorkID {
			index = i
			break
		}
	}
	if index < 0 {
		return errors.New("作品目录续办位置已失效")
	}
	if job.Mode == "batch" || job.State == "arming" {
		job.CompletedWorks++
	}
	if index+1 == len(works) {
		if job.Mode == "batch" {
			job.State = "complete"
			job.WorkID, job.Cursor = "", ""
			job.RootIDs, job.RootIndex, job.ReplyCursor, job.NextCursor, job.MainComplete = nil, 0, "", "", false
			return nil
		} else if job.State == "arming" {
			job.State = "active"
		}
		index = -1
	}
	job.WorkID, job.Cursor = works[index+1], ""
	job.RootIDs, job.RootIndex, job.ReplyCursor, job.NextCursor, job.MainComplete = nil, 0, "", "", false
	return nil
}

// One step handles one main-comment page or one reply page. The job cursor is
// committed only after the page is processed; the shared submit ledger makes
// a repeated page safe after a crash or cancellation.
func (m *localDouyinWebLogin) commentLikeStep(ctx context.Context, mode string) (bool, error) {
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		return false, errors.New("主人身份不可用")
	}
	fish, owner := identity.FishID, identity.LocalOwner.ID
	catalog, record, _, err := m.commentLikeCatalog(ctx, fish, owner, mode == "auto")
	if err != nil {
		if errors.Is(err, errLocalDouyinNoticeGrant) {
			return false, nil
		}
		return false, err
	}
	works := commentLikeCatalogWorks(catalog)
	if len(works) == 0 {
		return false, nil
	}
	db, err := openDouyinAutoCommentLikes(ctx, m.home, fish, true)
	if err != nil {
		return false, err
	}
	defer db.Close()
	lock, err := hostfile.TryLock(filepath.Join(filepath.Dir(douyinAutoCommentLikesPath(m.home, fish)), "auto-comment-likes.scan.lock"))
	if err != nil {
		return false, err
	}
	defer lock()
	a := (&localDouyinWebObservation{home: m.home}).access(fish, owner, record, localDouyinWebView{})
	profile, grant := a.connection.AccountID, douyinWorksCatalogGrant(record)
	hash := commentLikeCatalogHash(works)
	job, found, err := loadDouyinCommentLikeJob(ctx, db, owner, profile, grant, catalog.AccountID, mode)
	if err != nil {
		return false, err
	}
	if mode == "batch" && (!found || job.State != "running") {
		return false, nil
	}
	if mode == "auto" && (!found || job.Epoch != record.Revision || job.CatalogHash != hash) {
		job = resetDouyinCommentLikeJob(mode, record, hash, works)
		if _, err := db.ExecContext(ctx, `DELETE FROM auto_seen WHERE owner=? AND profile=? AND grant=? AND account=? AND epoch=?`,
			owner, profile, grant, catalog.AccountID, int64(record.Revision)); err != nil {
			return false, err
		}
		if err := saveDouyinCommentLikeJob(ctx, db, owner, profile, grant, catalog.AccountID, job); err != nil {
			return false, err
		}
	}
	if mode == "batch" && job.CatalogHash != hash {
		job = resetDouyinCommentLikeJob(mode, record, hash, works)
		if err := saveDouyinCommentLikeJob(ctx, db, owner, profile, grant, catalog.AccountID, job); err != nil {
			return false, err
		}
	}
	if job.WorkID == "" {
		return false, nil
	}
	action := "batch"
	if mode == "auto" {
		action = "auto"
		if job.State == "arming" {
			action = "baseline"
		}
	}
	input := localDouyinAutoCommentLikeInput{Action: action, Revision: record.Revision, WorkID: job.WorkID}
	if len(job.RootIDs) == 0 {
		input.Cursor = job.Cursor
	} else {
		if job.RootIndex < 0 || job.RootIndex >= len(job.RootIDs) {
			return false, errors.New("回复分页位置损坏")
		}
		input.RootCommentID, input.Cursor = job.RootIDs[job.RootIndex], job.ReplyCursor
	}
	result, _, err := m.autoCommentLikeRun(ctx, fish, owner, input)
	if err != nil {
		if errors.Is(err, errLocalDouyinLikeCancelled) {
			return true, nil
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if mode == "batch" {
			running, checkErr := commentLikeBatchIsRunning(ctx, db, owner, profile, grant, catalog.AccountID)
			if checkErr != nil {
				return true, checkErr
			}
			if !running {
				return true, nil
			}
			job.State, job.LastError = "error", err.Error()
			_ = saveDouyinCommentLikeJob(ctx, db, owner, profile, grant, catalog.AccountID, job)
		} else {
			job.LastError = err.Error()
			_ = saveDouyinCommentLikeJob(ctx, db, owner, profile, grant, catalog.AccountID, job)
		}
		return true, err
	}
	if mode == "batch" {
		running, err := commentLikeBatchIsRunning(ctx, db, owner, profile, grant, catalog.AccountID)
		if err != nil || !running {
			return true, err
		}
	}
	job.Seen += result.Seen
	job.LastError = ""
	job.Submitted += result.Submitted
	job.Unverified += result.Unverified
	if len(job.RootIDs) == 0 {
		job.RootIDs = result.PageCommentIDs
		job.RootIndex = 0
		job.NextCursor, job.MainComplete = result.NextCursor, result.PageEnd
		if len(job.RootIDs) == 0 {
			if err := advanceDouyinCommentLikeJob(&job, works); err != nil {
				return true, err
			}
		}
	} else if result.PageEnd {
		job.RootIndex++
		job.ReplyCursor = ""
		if job.RootIndex == len(job.RootIDs) {
			if err := advanceDouyinCommentLikeJob(&job, works); err != nil {
				return true, err
			}
		}
	} else {
		if result.NextCursor == "" || result.NextCursor == job.ReplyCursor {
			return true, errors.New("评论回复续页位置未推进")
		}
		job.ReplyCursor = result.NextCursor
	}
	if err := saveDouyinCommentLikeJob(ctx, db, owner, profile, grant, catalog.AccountID, job); err != nil {
		return true, err
	}
	return true, nil
}
