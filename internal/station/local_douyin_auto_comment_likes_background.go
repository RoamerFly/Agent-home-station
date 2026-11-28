package station

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	"wechat-bot/internal/hostfile"
)

// Each pass advances one page under one current work. The cursor is private
// and survives a restart; the last page advances to the next work, eventually
// wrapping to the first so newly arrived comments are observed.
func (m *localDouyinWebLogin) autoCommentLikeStep(ctx context.Context) error {
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		return errors.New("主人身份不可用")
	}
	fish, owner := identity.FishID, identity.LocalOwner.ID
	catalog, record, _, err := m.autoCommentLikeCatalog(ctx, fish, owner)
	if err != nil {
		return err
	}
	works := make([]string, 0, len(catalog.Works))
	for _, work := range catalog.Works {
		if douyinAutoCommentLikeWorkReason(work) == "" {
			works = append(works, work.ID)
		}
	}
	if len(works) == 0 {
		return nil
	}
	lock, err := hostfile.TryLock(filepath.Join(filepath.Dir(douyinAutoCommentLikesPath(m.home, fish)), "auto-comment-likes.scan.lock"))
	if err != nil {
		return err
	}
	defer lock()
	db, err := openDouyinAutoCommentLikes(ctx, m.home, fish, true)
	if err != nil {
		return err
	}
	defer db.Close()
	a := (&localDouyinWebObservation{home: m.home}).access(fish, owner, record, localDouyinWebView{})
	profile, grant := a.connection.AccountID, douyinWorksCatalogGrant(record)
	var savedWork, cursor string
	err = db.QueryRowContext(ctx, `SELECT work_id,cursor FROM scan_progress
WHERE owner=? AND profile=? AND grant=? AND account=?`, owner, profile, grant, catalog.AccountID).Scan(&savedWork, &cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	index := 0
	for i, work := range works {
		if work == savedWork {
			index = i
			break
		}
	}
	if savedWork != works[index] {
		cursor = ""
	}
	previousCursor := cursor
	result, _, err := m.autoCommentLikeRun(ctx, fish, owner, localDouyinAutoCommentLikeInput{
		Action: "run", Revision: record.Revision, WorkID: works[index], Cursor: cursor,
	})
	if err != nil {
		return err
	}
	if result.MainListEnd {
		index = (index + 1) % len(works)
		cursor = ""
	} else {
		cursor = result.NextCursor
		if cursor == "" || cursor == previousCursor {
			return errors.New("评论续页位置未推进")
		}
	}
	// Authorization and catalog generation may have changed during the page.
	current, currentRecord, _, err := m.autoCommentLikeCatalog(ctx, fish, owner)
	if err != nil || currentRecord != record || current.AccountID != catalog.AccountID {
		return errLocalDouyinNoticeGrant
	}
	_, err = db.ExecContext(ctx, `INSERT INTO scan_progress(owner,profile,grant,account,work_id,cursor,updated_at)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(owner,profile,grant,account) DO UPDATE SET
work_id=excluded.work_id,cursor=excluded.cursor,updated_at=excluded.updated_at`,
		owner, profile, grant, catalog.AccountID, works[index], cursor, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (m *localDouyinWebLogin) autoCommentLikeBackground() {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-timer.C:
		}
		runCtx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
		err := m.autoCommentLikeStep(runCtx)
		cancel()
		wait := 45 * time.Second
		if err != nil && !errors.Is(err, errLocalDouyinNoticeGrant) {
			wait = 4 * time.Minute
		}
		timer.Reset(wait)
	}
}
