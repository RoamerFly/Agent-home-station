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
