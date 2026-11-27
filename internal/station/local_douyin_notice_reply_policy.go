package station

import (
	"crypto/sha256"
	"encoding/hex"

	"wechat-bot/internal/stationbrowser"
)

// localDouyinNoticeReplyProof is the input contract for a notification-driven
// reply. A saved notice supplies only candidate IDs; the target and direct
// parent must also come from current structured comment/reply responses for the
// exact work. This predicate cannot authorize a production reply on its own.
type localDouyinNoticeReplyProof struct {
	NoticeID              string
	NoticeType            int
	NoticeWorkID          string
	CandidateSource       string
	DetailStatus          string
	DetailID              string
	DetailWorkID          string
	DetailCID             string
	TargetPath            string
	RootPath              string
	RootCandidateID       string
	Candidates            []stationbrowser.DouyinNoticeCandidate
	CreatorUID            string
	NoticeCreatorAccount  string
	WorksCreatorAccount   string
	CurrentCreatorAccount string
	WorkID                string
	ContextReady          bool
	Target                localDouyinReplyComment
	Root                  *localDouyinReplyComment
	Parent                *localDouyinReplyComment
}

// ReplyID identifies a top-level thread ("0" for a top-level comment).
// ReplyToReplyID identifies the direct child parent ("0" when absent).
// A rendered comment card cannot supply these fields reliably.
type localDouyinReplyComment struct {
	CID            string
	WorkID         string
	ReplyID        string
	ReplyToReplyID string
	AuthorUID      string
}

type localDouyinNoticeReplyDecision struct {
	Eligible bool
	Reason   string
	CID      string
}

func classifyLocalDouyinNoticeReply(p localDouyinNoticeReplyProof) localDouyinNoticeReplyDecision {
	reject := func(reason string) localDouyinNoticeReplyDecision {
		return localDouyinNoticeReplyDecision{Reason: reason}
	}
	if !validLocalDouyinCommentID(p.NoticeID) || !validLocalDouyinCommentID(p.WorkID) ||
		p.NoticeWorkID != p.WorkID || !validLocalDouyinCommentID(p.Target.CID) ||
		p.Target.WorkID != p.WorkID || !validLocalDouyinCommentID(p.Target.AuthorUID) {
		return reject("target_unverified")
	}
	if !verifiedLocalDouyinNoticeReplyCandidate(p) {
		return reject("target_unverified")
	}
	if !validDouyinNoticeAccount(p.NoticeCreatorAccount) || p.NoticeCreatorAccount != p.WorksCreatorAccount ||
		p.NoticeCreatorAccount != p.CurrentCreatorAccount {
		return reject("work_owner_unverified")
	}
	if p.CreatorUID != "" {
		if !validLocalDouyinCommentID(p.CreatorUID) {
			return reject("work_owner_unverified")
		}
		creatorDigest := sha256.Sum256([]byte(p.CreatorUID))
		if hex.EncodeToString(creatorDigest[:]) != p.CurrentCreatorAccount {
			return reject("work_owner_unverified")
		}
	}
	targetDigest := sha256.Sum256([]byte(p.Target.AuthorUID))
	if hex.EncodeToString(targetDigest[:]) == p.CurrentCreatorAccount {
		return reject("target_unverified")
	}
	if !p.ContextReady {
		return reject("work_context_incomplete")
	}
	if p.Target.ReplyID == "0" && p.Target.ReplyToReplyID == "0" {
		if p.Root != nil || p.Parent != nil || p.RootPath != "" {
			return reject("comment_thread_ambiguous")
		}
		return localDouyinNoticeReplyDecision{Eligible: true, Reason: "top_level_comment", CID: p.Target.CID}
	}
	if !validLocalDouyinCommentID(p.Target.ReplyID) || !validLocalDouyinCommentID(p.Target.ReplyToReplyID) ||
		p.Target.ReplyID == p.Target.CID || p.Target.ReplyToReplyID == p.Target.CID ||
		p.Target.ReplyID == p.Target.ReplyToReplyID || p.Root == nil || p.Parent == nil {
		return reject("direct_parent_unverified")
	}
	if p.RootPath != "" && p.RootCandidateID != p.Target.ReplyID {
		return reject("direct_parent_unverified")
	}
	if p.CandidateSource == "notice_list" {
		for _, candidate := range p.Candidates {
			if candidate.Path == "comment.parent_id" && candidate.ID != p.Target.ReplyID {
				return reject("direct_parent_unverified")
			}
		}
	}
	root, parent := *p.Root, *p.Parent
	if root.CID != p.Target.ReplyID || root.WorkID != p.WorkID || root.ReplyID != "0" || root.ReplyToReplyID != "0" ||
		!validLocalDouyinCommentID(root.AuthorUID) ||
		parent.CID != p.Target.ReplyToReplyID || parent.WorkID != p.WorkID || parent.ReplyID != root.CID ||
		parent.CID == p.Target.CID || !validLocalDouyinCommentID(parent.AuthorUID) {
		return reject("direct_parent_not_self_child")
	}
	parentDigest := sha256.Sum256([]byte(parent.AuthorUID))
	if hex.EncodeToString(parentDigest[:]) != p.CurrentCreatorAccount {
		return reject("direct_parent_not_self_child")
	}
	if parent.ReplyToReplyID != "0" && !validLocalDouyinCommentID(parent.ReplyToReplyID) {
		return reject("direct_parent_unverified")
	}
	return localDouyinNoticeReplyDecision{Eligible: true, Reason: "reply_to_self_child", CID: p.Target.CID}
}

func verifiedLocalDouyinNoticeReplyCandidate(p localDouyinNoticeReplyProof) bool {
	if !validDouyinRoundRelationTargetPath(p.TargetPath) || len(p.Candidates) == 0 || len(p.Candidates) > 16 ||
		(p.RootPath == "") != (p.RootCandidateID == "") ||
		p.RootPath != "" && (!validDouyinRoundRelationRootPath(p.RootPath) || !validLocalDouyinCommentID(p.RootCandidateID)) {
		return false
	}
	seen := make(map[string]struct{}, len(p.Candidates))
	targets, roots := 0, 0
	for _, candidate := range p.Candidates {
		if !validDouyinRoundCandidate(candidate) {
			return false
		}
		if _, duplicate := seen[candidate.Path]; duplicate {
			return false
		}
		seen[candidate.Path] = struct{}{}
		if candidate.Path == p.TargetPath && candidate.ID == p.Target.CID {
			targets++
		}
		if p.RootPath != "" && candidate.Path == p.RootPath && candidate.ID == p.RootCandidateID {
			roots++
		}
		if p.CandidateSource == "notice_list" && candidate.Path != "comment.comment.cid" && candidate.Path != "comment.parent_id" {
			return false
		}
		if p.CandidateSource == "notice_list" && candidate.Path == "comment.comment.cid" && candidate.ID != p.Target.CID ||
			p.CandidateSource == "notice_detail" && validDouyinRoundRelationTargetPath(candidate.Path) && candidate.ID != p.Target.CID {
			return false
		}
	}
	if targets != 1 || p.RootPath != "" && roots != 1 {
		return false
	}
	switch p.CandidateSource {
	case "notice_detail":
		return p.DetailStatus == "observed_candidate" && p.DetailID == p.NoticeID &&
			p.DetailWorkID == p.WorkID && p.DetailCID == p.Target.CID
	case "notice_list":
		return p.NoticeType == 31 && (p.DetailStatus == "not_observed" || p.DetailStatus == "partial") &&
			p.DetailID == "" && p.DetailWorkID == "" && p.DetailCID == "" &&
			p.TargetPath == "comment.comment.cid" && (p.RootPath == "" || p.RootPath == "comment.parent_id")
	}
	return false
}
