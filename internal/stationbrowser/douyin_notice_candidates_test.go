package stationbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDouyinNoticeCandidatesAreBoundedUnclassifiedAndRedacted(t *testing.T) {
	raw := []byte(`{"status_code":0,"has_more":0,"notice_list_v2":[{
		"nid_str":"123456","type":31,"create_time":1700000000,"aweme_id":"7391234567890123456",
		"cid":111,"real_cid":"111","reply_id":"0","reply_to_reply_id":null,
		"user":{"uid":321,"nickname":"synthetic-private-name"},
		"comment":{"cid":"111","reply_id":"222","reply_to_reply_id":0,
			"user":{"uid":"333","sec_uid":"synthetic-private-sec-uid"},"text":"synthetic-private-text"},
		"reply_comment":{"cid":"444","reply_id":"222","reply_to_reply_id":"555",
			"user":{"uid":666},"avatar_url":"https://example.invalid/synthetic-private-avatar"}
	}]}`)
	page, err := parseDouyinNoticePage(context.Background(), raw)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("notice page: %+v %v", page, err)
	}
	want := []DouyinNoticeCandidate{
		{Path: "cid", ID: "111"},
		{Path: "real_cid", ID: "111"},
		{Path: "user.uid", ID: "321"},
		{Path: "comment.cid", ID: "111"},
		{Path: "comment.reply_id", ID: "222"},
		{Path: "comment.user.uid", ID: "333"},
		{Path: "reply_comment.cid", ID: "444"},
		{Path: "reply_comment.reply_id", ID: "222"},
		{Path: "reply_comment.reply_to_reply_id", ID: "555"},
		{Path: "reply_comment.user.uid", ID: "666"},
	}
	if !reflect.DeepEqual(page.Items[0].Candidates, want) {
		t.Fatalf("candidate paths changed: got %#v, want %#v", page.Items[0].Candidates, want)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"synthetic-private-name", "synthetic-private-sec-uid", "synthetic-private-text", "synthetic-private-avatar"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private response field retained: %s", private)
		}
	}
	// Source object order is irrelevant; a continuation digest must use the
	// stable candidate path order, not the platform's JSON member order.
	reordered := []byte(`{"notice_list_v2":[{"reply_comment":{"user":{"uid":666},"reply_to_reply_id":"555","reply_id":"222","cid":"444"},"comment":{"user":{"uid":"333"},"reply_to_reply_id":0,"reply_id":"222","cid":"111"},"user":{"uid":321},"reply_to_reply_id":null,"reply_id":"0","real_cid":"111","cid":111,"aweme_id":"7391234567890123456","create_time":1700000000,"type":31,"nid_str":"123456"}],"has_more":0,"status_code":0}`)
	other, err := parseDouyinNoticePage(context.Background(), reordered)
	if err != nil || !reflect.DeepEqual(other.Items[0], page.Items[0]) {
		t.Fatalf("JSON object order changed candidates: %+v %v", other.Items, err)
	}
	position := noticePosition{group: "2"}
	if noticeCheckpoint(DouyinNoticeCheckpoint{}, position, page) != noticeCheckpoint(DouyinNoticeCheckpoint{}, position, other) {
		t.Fatal("equivalent structures produced different continuation digests")
	}
}

func TestDouyinNoticeCandidatesFailClosedOnAmbiguousFields(t *testing.T) {
	base := func(item string) []byte {
		return []byte(`{"status_code":0,"has_more":0,"notice_list_v2":[` + item + `]}`)
	}
	for name, item := range map[string]string{
		"empty ID":            `{"nid_str":"11","type":31,"cid":""}`,
		"zero ID":             `{"nid_str":"11","type":31,"cid":0}`,
		"boolean ID":          `{"nid_str":"11","type":31,"cid":true}`,
		"duplicate nested ID": `{"nid_str":"11","type":31,"cid":"12","comment":{"cid":"13","cid":"14"}}`,
		"malformed nested ID": `{"nid_str":"11","type":31,"cid":"12","comment":{"cid":"1e2"}}`,
		"fractional ID":       `{"nid_str":"11","type":31,"cid":12.5}`,
		"exponent ID":         `{"nid_str":"11","type":31,"cid":1e2}`,
		"leading zero ID":     `{"nid_str":"11","type":31,"cid":"012"}`,
		"oversized ID":        `{"nid_str":"11","type":31,"cid":"123456789012345678901"}`,
		"unexpected object":   `{"nid_str":"11","type":31,"cid":"12","reply_comment":[{"cid":"13"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			page, err := parseDouyinNoticePage(context.Background(), base(item))
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != "11" || page.Items[0].Type != 31 || len(page.Items[0].Candidates) != 0 {
				t.Fatalf("ambiguous candidate survived: %+v %v", page, err)
			}
		})
	}
	for _, item := range []string{
		`{"nid_str":"11","nid_str":"12","type":31,"cid":"13"}`,
		`{"nid_str":"11","type":31,"cid":"12","cid":"13"}`,
	} {
		page, err := parseDouyinNoticePage(context.Background(), base(item))
		if err != nil || len(page.Items) != 1 || len(page.Items[0].Candidates) != 0 {
			t.Fatalf("duplicate top-level member produced candidates: %s %+v %v", item, page, err)
		}
	}
	for _, raw := range [][]byte{
		[]byte(`{"status_code":0,"status_code":1,"has_more":0,"notice_list_v2":[]}`),
		[]byte(`{"status_code":0,"has_more":0,"notice_list_v2":[],"notice_list_v2":[{"nid_str":"11","type":31}]}`),
	} {
		if _, err := parseDouyinNoticePage(context.Background(), raw); !errors.Is(err, ErrDouyinNoticeUnavailable) {
			t.Fatalf("duplicate response key accepted: %s %v", raw, err)
		}
	}
}
