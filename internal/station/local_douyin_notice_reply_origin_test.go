package station

import (
	"encoding/json"
	"strings"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinNoticeReplyOriginBindsTargetAndThread(t *testing.T) {
	account, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
	base := localDouyinNoticeReplyOrigin{Kind: localDouyinNoticeReplyOriginKind, Version: 1,
		Date: "2026-11-27", NoticeID: "100", WorkID: "200", CreatorAccount: account,
		NoticeEvidenceKey: strings.Repeat("c", 64), CandidateSource: "notice_list",
		TargetPath: "comment.comment.cid", TargetID: "300", ThreadState: "top_level_observed",
		TargetAuthorDigest: other, TargetTextSHA: strings.Repeat("d", 64),
		MediaSHA: strings.Repeat("e", 64), CreatorKey: strings.Repeat("f", 64),
		ContextEpoch: "epoch", ContextGrant: strings.Repeat("1", 64),
		WorksScope: strings.Repeat("2", 64), CommentsScope: strings.Repeat("3", 64), RecordRevision: 1}
	draft := apphost.InteractionDraft{Target: apphost.ConnectionRef{AppID: douyinapp.ID}, Kind: "reply_comment",
		ContentID: douyinapp.WebCreatorWorkPrefix + "token", CommentID: "300", CommentVersion: base.TargetTextSHA}
	if !validLocalDouyinNoticeReplyOrigin(base, draft) {
		t.Fatal("valid top-level notice origin was rejected")
	}
	raw, err := json.Marshal(base)
	var decoded localDouyinNoticeReplyOrigin
	if err != nil || strictLocalMemoryDecode(raw, &decoded) != nil || !validLocalDouyinNoticeReplyOrigin(decoded, draft) {
		t.Fatalf("durable origin did not round-trip: %s %v", raw, err)
	}
	child := base
	child.ThreadState, child.RootID, child.ParentID, child.ParentAuthorDigest = "self_child_observed", "400", "500", account
	if !validLocalDouyinNoticeReplyOrigin(child, draft) {
		t.Fatal("derived root from exact target relation was rejected")
	}
	child.RootPath = "comment.parent_id"
	if !validLocalDouyinNoticeReplyOrigin(child, draft) {
		t.Fatal("saved root candidate was rejected")
	}
	for _, broken := range []localDouyinNoticeReplyOrigin{
		func() localDouyinNoticeReplyOrigin { x := base; x.Kind = ""; return x }(),
		func() localDouyinNoticeReplyOrigin { x := base; x.TargetID = "301"; return x }(),
		func() localDouyinNoticeReplyOrigin { x := base; x.TargetAuthorDigest = account; return x }(),
		func() localDouyinNoticeReplyOrigin { x := child; x.ParentAuthorDigest = other; return x }(),
		func() localDouyinNoticeReplyOrigin { x := child; x.ParentID = "400"; return x }(),
		func() localDouyinNoticeReplyOrigin { x := base; x.RootID = "400"; return x }(),
	} {
		if validLocalDouyinNoticeReplyOrigin(broken, draft) {
			t.Fatalf("unbound origin was accepted: %+v", broken)
		}
	}
}

func TestLocalDouyinNoticeReplyEvidenceKeyChangesWithCandidate(t *testing.T) {
	evidence := localDouyinExecutionNoticeEvidence{observedDate: "2026-11-27", creatorAccount: strings.Repeat("a", 64),
		round: localDouyinExecutionRound{NoticeID: "100", Type: 31, WorkID: "200", DetailStatus: "not_observed",
			Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "comment.comment.cid", ID: "300"}}}}
	first := localDouyinNoticeReplyEvidenceKey(evidence)
	evidence.round.Candidates = []stationbrowser.DouyinNoticeCandidate{{Path: "comment.comment.cid", ID: "301"}}
	if first == localDouyinNoticeReplyEvidenceKey(evidence) {
		t.Fatal("changed notice target retained the same evidence key")
	}
}
