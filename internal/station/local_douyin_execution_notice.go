package station

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"

	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/stationbrowser"
)

// localDouyinExecutionNoticeEvidence is a single saved notification, not a
// reply proof. In particular, candidate IDs have not been matched to a target
// comment or its direct parent. Keep the creator-account digest private.
type localDouyinExecutionNoticeEvidence struct {
	round          localDouyinExecutionRound
	observedDate   string
	creatorAccount string
}

func validLocalDouyinExecutionNoticeID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n")
}

// readExecutionNotice reads one exact row from the caller's current execution
// scope. It never opens Douyin or infers a reply target from candidate fields.
// The caller obtains the scope through executionRoundReadScope and rechecks its
// returned authorization function before using the result.
func (m *localDouyinWebLogin) readExecutionNotice(ctx context.Context, scope, noticeID string) (localDouyinExecutionNoticeEvidence, bool, error) {
	var evidence localDouyinExecutionNoticeEvidence
	if scope == "" || !validLocalDouyinExecutionNoticeID(noticeID) {
		return evidence, false, nil
	}
	db, err := openLocalDouyinExecutionDB(ctx, m.home, false)
	if errors.Is(err, os.ErrNotExist) {
		return evidence, false, nil
	}
	if err != nil {
		return evidence, false, err
	}
	defer db.Close()

	var round localDouyinExecutionRound
	err = db.QueryRowContext(ctx, `SELECT notice_id,type,work_id,created_at,observed_at,observed_date
FROM rounds WHERE scope=? AND notice_id=?`, scope, noticeID).
		Scan(&round.NoticeID, &round.Type, &round.WorkID, &round.CreatedAt, &round.ObservedAt, &evidence.observedDate)
	if errors.Is(err, sql.ErrNoRows) {
		return localDouyinExecutionNoticeEvidence{}, false, nil
	}
	if err != nil {
		return localDouyinExecutionNoticeEvidence{}, false, err
	}
	day, err := time.Parse("2006-01-02", evidence.observedDate)
	if err != nil || day.Format("2006-01-02") != evidence.observedDate {
		return localDouyinExecutionNoticeEvidence{}, false, errors.New("抖音执行回合首次观察日期无效")
	}
	round.State, round.Reason = "待核对", "证据不足"
	round.Candidates = []stationbrowser.DouyinNoticeCandidate{}
	round.DetailStatus = "not_observed"
	round.DetailCandidates = []stationbrowser.DouyinNoticeCandidate{}
	rows := []localDouyinExecutionRound{round}
	if err := readLocalDouyinRoundCandidates(ctx, db, scope, rows); err != nil {
		return localDouyinExecutionNoticeEvidence{}, false, err
	}
	if err := readLocalDouyinRoundDetails(ctx, db, scope, rows); err != nil {
		return localDouyinExecutionNoticeEvidence{}, false, err
	}
	// Older ledgers have no account table. Missing account evidence stays empty.
	var accountTable int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='round_creator_accounts'`).Scan(&accountTable); err != nil {
		return localDouyinExecutionNoticeEvidence{}, false, err
	}
	if accountTable != 0 {
		err = db.QueryRowContext(ctx, `SELECT creator_account FROM round_creator_accounts WHERE scope=? AND notice_id=?`, scope, noticeID).
			Scan(&evidence.creatorAccount)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return localDouyinExecutionNoticeEvidence{}, false, err
		}
		if evidence.creatorAccount != "" && !validDouyinNoticeAccount(evidence.creatorAccount) {
			return localDouyinExecutionNoticeEvidence{}, false, errors.New("抖音执行回合创作者账号证据无效")
		}
	}
	evidence.round = rows[0]
	// A saved detail, even with a unique candidate, cannot grant a reply.
	evidence.round.ReplyEligible = false
	return evidence, true, nil
}

// localDouyinNoticeReplyRequestKey is the eventual interaction actor key for
// one browser-verified creator account and notice. The caller must first prove
// that the digest came from the saved notice and still matches the current
// account. The key intentionally excludes notice epochs, chat events and text,
// so a retry can find the original durable job instead of creating another.
func localDouyinNoticeReplyRequestKey(fish, owner, creatorAccount, noticeID string) (string, bool) {
	if !fishidentity.ValidFishID(fish) || owner == "" || len(owner) > 128 ||
		!validDouyinNoticeAccount(creatorAccount) || !validLocalDouyinExecutionNoticeID(noticeID) {
		return "", false
	}
	return localCheckpointKey("douyin-notice-reply-v1", fish, owner, creatorAccount, noticeID), true
}
