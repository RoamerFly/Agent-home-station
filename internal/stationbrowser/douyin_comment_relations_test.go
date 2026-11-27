package stationbrowser

import "testing"

func TestParseDouyinCommentRelationsKeepsExactParentage(t *testing.T) {
	root := []byte(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"主评论"}]}`)
	got, err := parseDouyinCommentRelations(root, "200", "")
	if err != nil || len(got) != 1 || got[0].CID != "400" || got[0].ReplyID != "0" || got[0].ReplyToReplyID != "0" || got[0].AuthorUID != "901" {
		t.Fatalf("root relation = %+v, %v", got, err)
	}
	replies := []byte(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"500","reply_id":"400","reply_to_reply_id":"0","user":{"uid":"900"},"text":"我的子评论"},{"aweme_id":"200","cid":"600","reply_id":"400","reply_to_reply_id":"500","user":{"uid":"901"},"text":"回复我的子评论"}]}`)
	got, err = parseDouyinCommentRelations(replies, "200", "400")
	if err != nil || len(got) != 2 || got[1].ReplyID != "400" || got[1].ReplyToReplyID != "500" || got[0].AuthorUID != "900" {
		t.Fatalf("reply relations = %+v, %v", got, err)
	}
}

func TestParseDouyinCommentListKeepsNestedReplyParentage(t *testing.T) {
	// Synthetic IDs and text mirror the observed list/reply nesting without
	// retaining any account, comment or session material from the sample.
	raw := []byte(`{"status_code":0,"has_more":0,"cursor":0,"comments":[{"aweme_id":"200","cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"主评论","reply_comment":[{"aweme_id":"200","cid":"500","reply_id":"400","reply_to_reply_id":"0","user":{"uid":"900"},"text":"作者子评论","reply_comment":[]},{"aweme_id":"200","cid":"600","reply_id":"400","reply_to_reply_id":"500","user":{"uid":"901"},"text":"回复作者子评论","reply_comment":[]}]},{"aweme_id":"200","cid":"700","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"902"},"text":"另一主评论","reply_comment":[]}]}`)
	got, err := parseDouyinCommentRelations(raw, "200", "")
	if err != nil || len(got) != 4 || got[1].CID != "500" || got[1].ReplyID != "400" ||
		got[2].CID != "600" || got[2].ReplyID != "400" || got[2].ReplyToReplyID != "500" ||
		got[2].WorkID != "200" || got[3].CID != "700" {
		t.Fatalf("nested list relations = %+v, %v", got, err)
	}
	bad := []byte(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"主评论","reply_comment":[{"aweme_id":"200","cid":"500","reply_id":"401","reply_to_reply_id":"0","user":{"uid":"900"},"text":"错误主楼"}]}]}`)
	if _, err := parseDouyinCommentRelations(bad, "200", ""); err == nil {
		t.Fatal("nested reply from a different root was accepted")
	}
}

func TestParseDouyinReplyPageUsesReplyIDRatherThanRootCommentID(t *testing.T) {
	// Observed reply-list pages use root_comment_id inconsistently. These
	// synthetic rows ensure it cannot override the actual root (reply_id) or
	// the direct parent (reply_to_reply_id).
	raw := []byte(`{"status_code":0,"cursor":3,"has_more":1,"comments":[{"aweme_id":"200","cid":"500","reply_id":"400","reply_to_reply_id":"0","root_comment_id":"500","reply_comment":null,"user":{"uid":"900"},"text":"作者子评论"},{"aweme_id":"200","cid":"600","reply_id":"400","reply_to_reply_id":"500","root_comment_id":"500","reply_comment":null,"user":{"uid":"901"},"text":"回复作者子评论"}]}`)
	relations, err := parseDouyinCommentRelations(raw, "200", "400")
	if err != nil || len(relations) != 2 || relations[0].CID != "500" || relations[0].ReplyID != "400" ||
		relations[1].CID != "600" || relations[1].ReplyID != "400" || relations[1].ReplyToReplyID != "500" {
		t.Fatalf("reply-list parentage = %+v, %v", relations, err)
	}
	page, err := parseDouyinCommentRelationPage(raw, relations)
	if err != nil || !page.HasMore || page.Cursor != "3" {
		t.Fatalf("reply-list page = %+v, %v", page, err)
	}
}

func TestParseDouyinCommentRelationsRejectsAmbiguousSource(t *testing.T) {
	tests := map[string]string{
		"wrong work":            `{"status_code":0,"comments":[{"aweme_id":"201","cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"评论"}]}`,
		"duplicate key":         `{"status_code":0,"comments":[{"aweme_id":"200","cid":"400","cid":"401","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"评论"}]}`,
		"wrong root":            `{"status_code":0,"comments":[{"aweme_id":"200","cid":"500","reply_id":"401","reply_to_reply_id":"0","user":{"uid":"901"},"text":"回复"}]}`,
		"missing direct parent": `{"status_code":0,"comments":[{"aweme_id":"200","cid":"500","reply_id":"400","user":{"uid":"901"},"text":"回复"}]}`,
		"fractional ID":         `{"status_code":0,"comments":[{"aweme_id":"200","cid":400.1,"reply_id":"400","reply_to_reply_id":"0","user":{"uid":"901"},"text":"回复"}]}`,
		"missing UID":           `{"status_code":0,"comments":[{"aweme_id":"200","cid":"500","reply_id":"400","reply_to_reply_id":"0","user":{},"text":"回复"}]}`,
		"trailing JSON":         `{"status_code":0,"comments":[]} {"status_code":0,"comments":[]}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDouyinCommentRelations([]byte(raw), "200", "400"); err == nil {
				t.Fatal("ambiguous relation accepted")
			}
		})
	}
}
