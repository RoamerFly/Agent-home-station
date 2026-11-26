package station

import (
	"crypto/sha256"
	"encoding/hex"
)

// localDouyinNoticeReplyProof is the input contract for a notification-driven
// reply. The notice target must come from a verified, single-notice detail,
// never from the uncalibrated candidate IDs saved by execution rounds. The
// comment rows must come from a current structured comment/reply response for
// this exact video. None of those sources is wired up yet, so this predicate
// cannot authorize a production reply on its own.
type localDouyinNoticeReplyProof struct {
	NoticeID              string
	NoticeWorkID          string
	DetailID              string
	DetailWorkID          string
	DetailCID             string
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
	if !validLocalDouyinCommentID(p.NoticeID) || p.DetailID != p.NoticeID ||
		!validLocalDouyinCommentID(p.WorkID) || p.NoticeWorkID != p.WorkID || p.DetailWorkID != p.WorkID ||
		!validLocalDouyinCommentID(p.DetailCID) || !validLocalDouyinCommentID(p.Target.CID) || p.DetailCID != p.Target.CID ||
		p.Target.WorkID != p.WorkID || !validLocalDouyinCommentID(p.CreatorUID) ||
		!validLocalDouyinCommentID(p.Target.AuthorUID) || p.Target.AuthorUID == p.CreatorUID {
		return reject("target_unverified")
	}
	if !validDouyinNoticeAccount(p.NoticeCreatorAccount) || p.NoticeCreatorAccount != p.WorksCreatorAccount ||
		p.NoticeCreatorAccount != p.CurrentCreatorAccount {
		return reject("work_owner_unverified")
	}
	creatorDigest := sha256.Sum256([]byte(p.CreatorUID))
	if hex.EncodeToString(creatorDigest[:]) != p.CurrentCreatorAccount {
		return reject("work_owner_unverified")
	}
	if !p.ContextReady {
		return reject("work_context_incomplete")
	}
	if p.Target.ReplyID == "0" && p.Target.ReplyToReplyID == "0" {
		if p.Root != nil || p.Parent != nil {
			return reject("comment_thread_ambiguous")
		}
		return localDouyinNoticeReplyDecision{Eligible: true, Reason: "top_level_comment", CID: p.Target.CID}
	}
	if !validLocalDouyinCommentID(p.Target.ReplyID) || !validLocalDouyinCommentID(p.Target.ReplyToReplyID) ||
		p.Target.ReplyID == p.Target.CID || p.Target.ReplyToReplyID == p.Target.CID ||
		p.Target.ReplyID == p.Target.ReplyToReplyID || p.Root == nil || p.Parent == nil {
		return reject("direct_parent_unverified")
	}
	root, parent := *p.Root, *p.Parent
	if root.CID != p.Target.ReplyID || root.WorkID != p.WorkID || root.ReplyID != "0" || root.ReplyToReplyID != "0" ||
		parent.CID != p.Target.ReplyToReplyID || parent.WorkID != p.WorkID || parent.ReplyID != root.CID ||
		parent.CID == p.Target.CID || parent.AuthorUID != p.CreatorUID {
		return reject("direct_parent_not_self_child")
	}
	if parent.ReplyToReplyID != "0" && !validLocalDouyinCommentID(parent.ReplyToReplyID) {
		return reject("direct_parent_unverified")
	}
	return localDouyinNoticeReplyDecision{Eligible: true, Reason: "reply_to_self_child", CID: p.Target.CID}
}
