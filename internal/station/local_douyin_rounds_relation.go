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
// Each exact target/root/parent probe inspects at most one naturally emitted
// page. A child may first appear in the main list's embedded reply preview;
// a missing row remains unverified and no comment thread is scanned.
type localDouyinRoundRelationResult struct {
	Date                  string `json:"date"`
	NoticeID              string `json:"notice_id"`
	WorkID                string `json:"work_id"`
	TargetPath            string `json:"target_path"`
	TargetID              string `json:"target_id"`
	CandidateSource       string `json:"candidate_source,omitempty"`
	RootPath              string `json:"root_path,omitempty"`
	RootID                string `json:"root_id,omitempty"`
	State                 string `json:"state"`
	StopReason            string `json:"stop_reason,omitempty"`
	CID                   string `json:"cid,omitempty"`
	ReplyID               string `json:"reply_id,omitempty"`
	ReplyToReplyID        string `json:"reply_to_reply_id,omitempty"`
	AuthorIsCreator       *bool  `json:"author_is_creator,omitempty"`
	RootState             string `json:"root_state,omitempty"`
	ParentState           string `json:"parent_state,omitempty"`
	ParentAuthorIsCreator *bool  `json:"parent_author_is_creator,omitempty"`
	ThreadState           string `json:"thread_state,omitempty"`
	Text                  string `json:"text,omitempty"`
	Coverage              string `json:"coverage"`
	ReplyEligible         bool   `json:"reply_eligible"`
	targetRow             stationbrowser.DouyinCommentRelation
	rootRow               *stationbrowser.DouyinCommentRelation
	parentRow             *stationbrowser.DouyinCommentRelation
}

func validDouyinRoundRelationTargetPath(path string) bool {
	switch path {
	case "cid", "real_cid", "comment.cid", "comment.comment.cid", "reply_comment.cid":
		return true
	}
	return false
}

func validDouyinRoundRelationRootPath(path string) bool {
	switch path {
	case "reply_id", "comment.parent_id", "comment.reply_id", "comment.comment.reply_id", "reply_comment.reply_id":
		return true
	}
	return false
}

func douyinRoundRelationCandidates(round localDouyinExecutionRound) ([]stationbrowser.DouyinNoticeCandidate, string, bool) {
	if round.DetailStatus == "observed_candidate" {
		for _, listed := range round.Candidates {
			for _, detailed := range round.DetailCandidates {
				if listed.Path == detailed.Path && listed.ID != detailed.ID {
					return nil, "", false
				}
			}
		}
		return round.DetailCandidates, "notice_detail", true
	}
	if (round.DetailStatus != "not_observed" && round.DetailStatus != "partial") || round.Type != 31 ||
		!validLocalDouyinCommentID(round.NoticeID) {
		return nil, "", false
	}
	targets := 0
	seenPaths := make(map[string]struct{}, len(round.Candidates))
	for _, candidate := range round.Candidates {
		if !validDouyinRoundCandidate(candidate) {
			return nil, "", false
		}
		if _, duplicate := seenPaths[candidate.Path]; duplicate {
			return nil, "", false
		}
		seenPaths[candidate.Path] = struct{}{}
		switch candidate.Path {
		case "comment.comment.cid":
			targets++
		case "comment.parent_id":
		default:
			return nil, "", false
		}
	}
	if targets != 1 {
		return nil, "", false
	}
	return round.Candidates, "notice_list", true
}

func douyinRoundCandidateMatches(candidates []stationbrowser.DouyinNoticeCandidate, path, id string) bool {
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
		if got.ReplyID == "0" {
			return got.ReplyToReplyID == "0"
		}
		// Without a root candidate, only the main list's embedded target row
		// can supply the root. The caller cannot issue a root reply-list query
		// before independently observing this exact notification CID.
		return validLocalDouyinCommentID(got.ReplyID) && got.ReplyToReplyID != got.CID &&
			(got.ReplyToReplyID == "0" || validLocalDouyinCommentID(got.ReplyToReplyID))
	}
	return got.ReplyID == in.RootID && got.ReplyToReplyID != got.CID &&
		(got.ReplyToReplyID == "0" || validLocalDouyinCommentID(got.ReplyToReplyID))
}

func (t *localToolSet) readDouyinRoundRelation(ctx context.Context, m *localDouyinWebLogin, record localDouyinWebRecord,
	profile, scope string, in localDouyinRoundsInput, notice localDouyinNoticeProof) (localDouyinRoundRelationResult, *localDouyinRoundContextProof, error) {
	result := localDouyinRoundRelationResult{Date: in.Date, NoticeID: in.NoticeID, TargetPath: in.TargetPath,
		TargetID: in.TargetID, RootPath: in.RootPath, RootID: in.RootID, State: "unverified",
		Coverage: "仅通知编号候选；一次页面自然加载的定向关系读取不证明回复资格", ReplyEligible: false}
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
	candidates, candidateSource, candidateValid := douyinRoundRelationCandidates(evidence.round)
	if !found || evidence.observedDate != in.Date || !candidateValid ||
		!validLocalDouyinCommentID(evidence.round.WorkID) || !validDouyinNoticeAccount(evidence.creatorAccount) ||
		!douyinRoundCandidateMatches(candidates, in.TargetPath, in.TargetID) ||
		in.RootID != "" && !douyinRoundCandidateMatches(candidates, in.RootPath, in.RootID) {
		return result, nil, mcptools.ArgumentError("$.notice_id", "所选通知没有可定向核对的对应编号；候选不能用于回复")
	}
	result.CandidateSource = candidateSource
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
	// One bounded main-list preview may contain the target child. Only if the
	// preview lacks it do we try the exact root reply list. Root and direct
	// parent remain separate exact probes, never a scan for notifications.
	readCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	stop := context.AfterFunc(serviceCtx, cancel)
	defer stop()
	readExact := func(rootID, targetID string) (stationbrowser.DouyinCommentRelation, error) {
		if err := check(); err != nil {
			return stationbrowser.DouyinCommentRelation{}, err
		}
		row, err := reader(readCtx, profile, stationbrowser.DouyinCommentRelationRequest{
			WorkID: result.WorkID, RootID: rootID, TargetID: targetID}, check)
		if checkErr := check(); checkErr != nil {
			return stationbrowser.DouyinCommentRelation{}, checkErr
		}
		if readCtx.Err() != nil {
			return stationbrowser.DouyinCommentRelation{}, stationbrowser.ErrDouyinCommentsUnavailable
		}
		return row, err
	}
	readChild := func(rootID, targetID string) (stationbrowser.DouyinCommentRelation, error) {
		row, err := readExact("", targetID)
		if errors.Is(err, stationbrowser.ErrDouyinCommentTargetPending) {
			return readExact(rootID, targetID)
		}
		return row, err
	}
	var got stationbrowser.DouyinCommentRelation
	var readErr error
	if in.RootID == "" {
		got, readErr = readExact("", in.TargetID)
	} else {
		got, readErr = readChild(in.RootID, in.TargetID)
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
	result.targetRow = got
	authorIsCreator := hex.EncodeToString(digest[:]) == evidence.creatorAccount
	result.AuthorIsCreator = &authorIsCreator
	result.Text = got.Text
	if got.ReplyID == "0" {
		result.ThreadState = "top_level_observed"
		result.Coverage = "通知指定的目标 CID 与主评论结构化关系已观察；不授权回复"
		return result, proof, nil
	}
	rootID := in.RootID
	if rootID == "" {
		rootID = got.ReplyID
	}
	if got.ReplyToReplyID == "0" || got.ReplyToReplyID == rootID {
		result.ThreadState = "direct_parent_is_root"
		result.Coverage = "目标子评论直接回复主楼；不是回复本账号子评论的情形；不授权回复"
		return result, proof, nil
	}
	classifyRead := func(err error, field *string, pendingReason string) error {
		if err == nil {
			return nil
		}
		switch {
		case errors.Is(err, stationbrowser.ErrDouyinLoginActive):
			*field, result.StopReason = "busy", "browser_profile_in_use"
		case errors.Is(err, stationbrowser.ErrDouyinCommentTargetPending):
			*field, result.StopReason = "pending", pendingReason
		default:
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, apphost.ErrObservationForbidden) || errors.Is(err, errLocalDouyinNoticeGrant) {
				return err
			}
			return stationbrowser.ErrDouyinCommentsUnavailable
		}
		return nil
	}
	root, readErr := readExact("", rootID)
	if err := classifyRead(readErr, &result.RootState, "root_not_in_natural_page"); err != nil {
		return result, nil, err
	}
	if readErr != nil {
		result.ThreadState = "unverified"
		result.Coverage = "目标评论已观察；主评论仍未核实；不能判断回复关系"
		return result, proof, nil
	}
	if root.CID != rootID || root.WorkID != result.WorkID || root.ReplyID != "0" ||
		root.ReplyToReplyID != "0" || !validLocalDouyinCommentID(root.AuthorUID) ||
		strings.TrimSpace(root.Text) == "" || len(root.Text) > 8192 {
		return result, nil, apphost.ErrObservationInvalid
	}
	result.RootState = "observed"
	result.rootRow = &root
	parent, readErr := readChild(rootID, got.ReplyToReplyID)
	if err := classifyRead(readErr, &result.ParentState, "parent_not_in_natural_page"); err != nil {
		return result, nil, err
	}
	if readErr != nil {
		result.ThreadState = "unverified"
		result.Coverage = "目标与主评论已观察；直接父评论仍未核实；不能判断回复关系"
		return result, proof, nil
	}
	if parent.CID != got.ReplyToReplyID || parent.WorkID != result.WorkID || parent.ReplyID != rootID ||
		parent.ReplyToReplyID == parent.CID ||
		parent.ReplyToReplyID != "0" && !validLocalDouyinCommentID(parent.ReplyToReplyID) ||
		!validLocalDouyinCommentID(parent.AuthorUID) || strings.TrimSpace(parent.Text) == "" || len(parent.Text) > 8192 {
		return result, nil, apphost.ErrObservationInvalid
	}
	result.ParentState = "observed"
	result.parentRow = &parent
	parentDigest := sha256.Sum256([]byte(parent.AuthorUID))
	parentIsCreator := hex.EncodeToString(parentDigest[:]) == evidence.creatorAccount
	result.ParentAuthorIsCreator = &parentIsCreator
	if parentIsCreator {
		result.ThreadState = "self_child_observed"
	} else {
		result.ThreadState = "other_child_observed"
	}
	result.Coverage = "仅逐条核对目标、主评论及直接父评论的结构化关系；工作台内通知到评论的完整链路未验收，不授权回复"
	return result, proof, nil
}
