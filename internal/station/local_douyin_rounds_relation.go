package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

// A relation probe never promotes a notification candidate to reply proof.
// Only one page naturally requested by the video page is inspected; a missing
// target, root or direct parent remains unverified.
type localDouyinRoundRelationResult struct {
	Date            string `json:"date"`
	NoticeID        string `json:"notice_id"`
	WorkID          string `json:"work_id"`
	TargetPath      string `json:"target_path"`
	TargetID        string `json:"target_id"`
	RootPath        string `json:"root_path,omitempty"`
	RootID          string `json:"root_id,omitempty"`
	State           string `json:"state"`
	StopReason      string `json:"stop_reason,omitempty"`
	CID             string `json:"cid,omitempty"`
	ReplyID         string `json:"reply_id,omitempty"`
	ReplyToReplyID  string `json:"reply_to_reply_id,omitempty"`
	AuthorIsCreator *bool  `json:"author_is_creator,omitempty"`
	Text            string `json:"text,omitempty"`
	Coverage        string `json:"coverage"`
	ReplyEligible   bool   `json:"reply_eligible"`
}

func validDouyinRoundRelationTargetPath(path string) bool {
	switch path {
	case "cid", "real_cid", "comment.cid", "reply_comment.cid":
		return true
	}
	return false
}

func validDouyinRoundRelationRootPath(path string) bool {
	switch path {
	case "reply_id", "comment.reply_id", "reply_comment.reply_id":
		return true
	}
	return false
}

func douyinRoundDetailCandidateMatches(candidates []stationbrowser.DouyinNoticeCandidate, path, id string) bool {
	for _, candidate := range candidates {
		if candidate.Path == path && candidate.ID == id {
			return true
		}
	}
	return false
}

func validDouyinRoundRelationResult(got stationbrowser.DouyinCommentRelation, in localDouyinRoundsInput, workID string) bool {
	if got.CID != in.TargetID || got.WorkID != workID || !validLocalDouyinCommentID(got.AuthorUID) ||
		strings.TrimSpace(got.Text) == "" || len(got.Text) > 8192 {
		return false
	}
	if in.RootID == "" {
		return got.ReplyID == "0" && got.ReplyToReplyID == "0"
	}
	return got.ReplyID == in.RootID && got.ReplyToReplyID != got.CID &&
		(got.ReplyToReplyID == "0" || validLocalDouyinCommentID(got.ReplyToReplyID))
}

func (t *localToolSet) readDouyinRoundRelation(ctx context.Context, m *localDouyinWebLogin, record localDouyinWebRecord,
	profile, scope string, in localDouyinRoundsInput, notice localDouyinNoticeProof) (localDouyinRoundRelationResult, *localDouyinRoundContextProof, error) {
	result := localDouyinRoundRelationResult{Date: in.Date, NoticeID: in.NoticeID, TargetPath: in.TargetPath,
		TargetID: in.TargetID, RootPath: in.RootPath, RootID: in.RootID, State: "unverified",
		Coverage: "仅通知详情的未校准候选；一次页面自然加载的定向关系读取不证明回复资格", ReplyEligible: false}
	works, granted := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryWorks)
	if !granted {
		return result, nil, apphost.ErrObservationForbidden
	}
	comments, granted := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryComments)
	if !granted {
		return result, nil, apphost.ErrObservationForbidden
	}
	evidence, found, err := m.readExecutionNotice(ctx, scope, in.NoticeID)
	if err != nil {
		return result, nil, err
	}
	if !found || evidence.observedDate != in.Date || evidence.round.DetailStatus != "observed_candidate" ||
		!validLocalDouyinCommentID(evidence.round.WorkID) || !validDouyinNoticeAccount(evidence.creatorAccount) ||
		!douyinRoundDetailCandidateMatches(evidence.round.DetailCandidates, in.TargetPath, in.TargetID) ||
		in.RootID != "" && !douyinRoundDetailCandidateMatches(evidence.round.DetailCandidates, in.RootPath, in.RootID) {
		return result, nil, mcptools.ArgumentError("$.notice_id", "所选通知详情没有可定向核对的对应编号；候选不能用于回复")
	}
	result.WorkID = evidence.round.WorkID
	proof := &localDouyinRoundContextProof{Source: works, CommentsSource: comments,
		Revision: record.Revision, Date: in.Date, WorkID: result.WorkID}
	access := (&localDouyinWebObservation{home: m.home}).access(t.access.fishID, t.access.ownerID, record, m.view(record))
	check := func() error {
		if err := m.ctx.Err(); err != nil {
			return err
		}
		if err := t.checkNoticeProof(ctx, notice); err != nil {
			return err
		}
		if err := t.checkDouyinRoundContextProof(ctx, *proof); err != nil {
			return err
		}
		if !creatorOwnerCurrent(access, evidence.creatorAccount) {
			return apphost.ErrObservationForbidden
		}
		latest, exists, err := m.readExecutionNotice(ctx, scope, in.NoticeID)
		if err != nil {
			return err
		}
		if !exists || !reflect.DeepEqual(latest, evidence) {
			return apphost.ErrObservationForbidden
		}
		return nil
	}
	if err := check(); err != nil {
		return result, nil, err
	}
	m.mu.Lock()
	reader, serviceCtx := m.readRelation, m.ctx
	m.mu.Unlock()
	if reader == nil {
		return result, nil, stationbrowser.ErrDouyinCommentsUnavailable
	}
	readCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	stop := context.AfterFunc(serviceCtx, cancel)
	defer stop()
	got, readErr := reader(readCtx, profile, stationbrowser.DouyinCommentRelationRequest{
		WorkID: result.WorkID, RootID: in.RootID, TargetID: in.TargetID}, check)
	if err := check(); err != nil {
		return result, nil, err
	}
	if readCtx.Err() != nil {
		return result, nil, stationbrowser.ErrDouyinCommentsUnavailable
	}
	if readErr != nil {
		switch {
		case errors.Is(readErr, stationbrowser.ErrDouyinLoginActive):
			result.State, result.StopReason = "busy", "browser_profile_in_use"
		case errors.Is(readErr, stationbrowser.ErrDouyinCommentTargetPending):
			result.State, result.StopReason = "pending", "target_not_in_natural_page"
		default:
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) ||
				errors.Is(readErr, apphost.ErrObservationForbidden) || errors.Is(readErr, errLocalDouyinNoticeGrant) {
				return result, nil, readErr
			}
			return result, nil, stationbrowser.ErrDouyinCommentsUnavailable
		}
		return result, proof, nil
	}
	if !validDouyinRoundRelationResult(got, in, result.WorkID) {
		return result, nil, apphost.ErrObservationInvalid
	}
	digest := sha256.Sum256([]byte(got.AuthorUID))
	result.State, result.CID, result.ReplyID, result.ReplyToReplyID = "observed", got.CID, got.ReplyID, got.ReplyToReplyID
	authorIsCreator := hex.EncodeToString(digest[:]) == evidence.creatorAccount
	result.AuthorIsCreator = &authorIsCreator
	result.Text = got.Text
	result.Coverage = "只核对通知详情候选指向的单条评论；主楼和直接父评论尚未核实，通知字段语义尚未校准"
	return result, proof, nil
}
