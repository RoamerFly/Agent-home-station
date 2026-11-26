package station

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestClassifyLocalDouyinNoticeReplyRequiresExactThreadAndOwner(t *testing.T) {
	creatorDigest := sha256.Sum256([]byte("900"))
	account := hex.EncodeToString(creatorDigest[:])
	base := localDouyinNoticeReplyProof{
		NoticeID: "100", NoticeWorkID: "200", DetailID: "100", DetailWorkID: "200", DetailCID: "300",
		CreatorUID: "900", NoticeCreatorAccount: account, WorksCreatorAccount: account, CurrentCreatorAccount: account,
		WorkID: "200", ContextReady: true,
		Target: localDouyinReplyComment{CID: "300", WorkID: "200", ReplyID: "0", ReplyToReplyID: "0", AuthorUID: "901"},
	}
	tests := []struct {
		name   string
		edit   func(*localDouyinNoticeReplyProof)
		want   bool
		reason string
	}{
		{"top-level", func(*localDouyinNoticeReplyProof) {}, true, "top_level_comment"},
		{"missing context", func(p *localDouyinNoticeReplyProof) { p.ContextReady = false }, false, "work_context_incomplete"},
		{"notice target mismatch", func(p *localDouyinNoticeReplyProof) { p.DetailCID = "301" }, false, "target_unverified"},
		{"notice work mismatch", func(p *localDouyinNoticeReplyProof) { p.DetailWorkID = "201" }, false, "target_unverified"},
		{"account mismatch", func(p *localDouyinNoticeReplyProof) { p.NoticeCreatorAccount = strings.Repeat("b", 64) }, false, "work_owner_unverified"},
		{"creator UID mismatch", func(p *localDouyinNoticeReplyProof) { p.CreatorUID = "899" }, false, "work_owner_unverified"},
		{"self target", func(p *localDouyinNoticeReplyProof) { p.Target.AuthorUID = "900" }, false, "target_unverified"},
		{"child of own child", func(p *localDouyinNoticeReplyProof) {
			p.Target.ReplyID, p.Target.ReplyToReplyID = "400", "500"
			p.Root = &localDouyinReplyComment{CID: "400", WorkID: "200", ReplyID: "0", ReplyToReplyID: "0", AuthorUID: "901"}
			p.Parent = &localDouyinReplyComment{CID: "500", WorkID: "200", ReplyID: "400", ReplyToReplyID: "0", AuthorUID: "900"}
		}, true, "reply_to_self_child"},
		{"child of another user", func(p *localDouyinNoticeReplyProof) {
			p.Target.ReplyID, p.Target.ReplyToReplyID = "400", "500"
			p.Root = &localDouyinReplyComment{CID: "400", WorkID: "200", ReplyID: "0", ReplyToReplyID: "0", AuthorUID: "901"}
			p.Parent = &localDouyinReplyComment{CID: "500", WorkID: "200", ReplyID: "400", ReplyToReplyID: "0", AuthorUID: "902"}
		}, false, "direct_parent_not_self_child"},
		{"root author missing", func(p *localDouyinNoticeReplyProof) {
			p.Target.ReplyID, p.Target.ReplyToReplyID = "400", "500"
			p.Root = &localDouyinReplyComment{CID: "400", WorkID: "200", ReplyID: "0", ReplyToReplyID: "0"}
			p.Parent = &localDouyinReplyComment{CID: "500", WorkID: "200", ReplyID: "400", ReplyToReplyID: "0", AuthorUID: "900"}
		}, false, "direct_parent_not_self_child"},
		{"reply to root", func(p *localDouyinNoticeReplyProof) {
			p.Target.ReplyID, p.Target.ReplyToReplyID = "400", "400"
			p.Root = &localDouyinReplyComment{CID: "400", WorkID: "200", ReplyID: "0", ReplyToReplyID: "0", AuthorUID: "900"}
			p.Parent = p.Root
		}, false, "direct_parent_unverified"},
		{"missing direct parent", func(p *localDouyinNoticeReplyProof) {
			p.Target.ReplyID, p.Target.ReplyToReplyID = "400", "500"
			p.Root = &localDouyinReplyComment{CID: "400", WorkID: "200", ReplyID: "0", ReplyToReplyID: "0", AuthorUID: "901"}
		}, false, "direct_parent_unverified"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.edit(&p)
			got := classifyLocalDouyinNoticeReply(p)
			if got.Eligible != tc.want || got.Reason != tc.reason || (tc.want && got.CID != p.Target.CID) || (!tc.want && got.CID != "") {
				t.Fatalf("classifyLocalDouyinNoticeReply() = %+v, want eligible=%t reason=%s", got, tc.want, tc.reason)
			}
		})
	}
}
