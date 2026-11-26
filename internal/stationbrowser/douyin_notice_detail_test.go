package stationbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestDouyinNoticeDetailRequiresKnownExactIDs(t *testing.T) {
	const origin = "https://www.douyin.com"
	known := map[string]int{"123456": 31, "654321": 33, "777777": 0}
	makeURL := func(ids string) string {
		return origin + douyinNoticeDetailPath + "?aid=6383&id_list=" + url.QueryEscape(ids)
	}
	refs, ok := noticeDetailRequest(makeURL(`[{"notice_id_str":"123456","type":0},{"notice_id_str":"654321","type":33}]`), origin, known)
	if !ok || len(refs) != 2 || refs[0].id != "123456" || refs[0].requestType != 0 || refs[1].id != "654321" {
		t.Fatalf("known details rejected: %+v %v", refs, ok)
	}
	if _, ok := noticeDetailRequest(makeURL(`[{"notice_id_str":"777777","type":0}]`), origin, known); !ok {
		t.Fatal("unclassified list type 0 lost its matching detail request")
	}
	page, err := parseDouyinNoticePage(context.Background(), []byte(`{"status_code":0,"has_more":0,"notice_list_v2":[{"nid_str":"777777","type":0}]}`))
	if err != nil || len(page.Items) != 1 || page.Items[0].Type != 0 {
		t.Fatalf("unclassified list notice was discarded: %+v %v", page, err)
	}
	for _, raw := range []string{
		makeURL(`[{"notice_id_str":"111111","type":0}]`),
		makeURL(`[{"notice_id_str":"123456","type":33}]`),
		makeURL(`[{"notice_id_str":"123456","type":0},{"notice_id_str":"123456","type":0}]`),
		makeURL(`[{"notice_id_str":"123456","type":0,"type":31}]`),
		makeURL(`[{"notice_id_str":"123456","type":0,"extra":"x"}]`),
		makeURL(`[{"notice_id_str":"123456","type":-1}]`),
		strings.Replace(makeURL(`[{"notice_id_str":"123456","type":0}]`), "aid=6383", "aid=6383&aid=6383", 1),
		strings.Replace(makeURL(`[{"notice_id_str":"123456","type":0}]`), origin, "https://creator.douyin.com", 1),
	} {
		if _, ok := noticeDetailRequest(raw, origin, known); ok {
			t.Errorf("invalid detail request admitted: %s", raw)
		}
	}
}

func TestDouyinNoticeDetailSingleIDRequestStaysBoundToKnownNotice(t *testing.T) {
	const origin = "https://www.douyin.com"
	known := map[string]int{"123456": 31, "777777": 0}
	makeURL := func(id string) string {
		return origin + douyinNoticeDetailPath + "?aid=6383&notice_id_str=" + url.QueryEscape(id)
	}
	refs, ok := noticeDetailRequest(makeURL("123456"), origin, known)
	if !ok || len(refs) != 1 || refs[0].id != "123456" || refs[0].requestType != 31 {
		t.Fatalf("known single detail rejected: %+v %v", refs, ok)
	}
	items, err := parseDouyinNoticeDetail(t.Context(), []byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"123456","type":31,"aweme_id":"200","comment":{"cid":"400"}}]}`), refs)
	if err != nil || len(items) != 1 || items[0].NoticeID != "123456" || len(items[0].Candidates) != 1 ||
		items[0].Candidates[0].Path != "comment.cid" || items[0].Candidates[0].ID != "400" {
		t.Fatalf("single detail was not normalized as an unclassified candidate: %+v %v", items, err)
	}
	if _, ok := noticeDetailRequest(makeURL("777777"), origin, known); !ok {
		t.Fatal("known notice with list type 0 lost single detail request")
	}
	for _, raw := range []string{
		makeURL("111111"),
		makeURL("not-a-number"),
		makeURL("123456") + "&notice_id_str=123456",
		makeURL("123456") + "&id_list=" + url.QueryEscape(`[{"notice_id_str":"123456","type":31}]`),
		strings.Replace(makeURL("123456"), "aid=6383", "aid=6383&aid=6383", 1),
		strings.Replace(makeURL("123456"), origin, "https://creator.douyin.com", 1),
	} {
		if _, ok := noticeDetailRequest(raw, origin, known); ok {
			t.Errorf("invalid single detail request admitted: %s", raw)
		}
	}
	if _, err := parseDouyinNoticeDetail(t.Context(), []byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"123456","type":33}]}`), refs); !errors.Is(err, ErrDouyinNoticeUnavailable) {
		t.Fatalf("different notification type accepted: %v", err)
	}
}

func TestDouyinNoticeDetailNormalizesPerNoticeWithoutReplyAuthority(t *testing.T) {
	refs := []noticeDetailRef{{id: "123456", requestType: 0}}
	raw := []byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"123456","type":31,"create_time":1700000000,"aweme_id":"7391234567890123456","cid":"111","real_cid":"222","comment":{"cid":"333","reply_id":"444","text":"private-comment"},"reply_comment":{"cid":"555","reply_to_reply_id":"444"},"from_user":{"uid":"private-uid"},"message_extra":"private-extra"}]}`)
	items, err := parseDouyinNoticeDetail(context.Background(), raw, refs)
	if err != nil || len(items) != 1 || items[0].NoticeID != "123456" || items[0].NoticeType != 31 || items[0].WorkID != "7391234567890123456" || !items[0].CandidateFieldsValid || len(items[0].Candidates) != 6 {
		t.Fatalf("detail normalization: %+v %v", items, err)
	}
	for _, candidate := range items[0].Candidates {
		if candidate.Path == "target_cid" {
			t.Fatal("unverified target classification appeared")
		}
	}
	encoded, _ := json.Marshal(items)
	for _, private := range []string{"private-comment", "private-uid", "private-extra"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private response leaked into detail: %s", private)
		}
	}
	for _, bad := range [][]byte{
		[]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"999999","type":31}]}`),
		[]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"123456","nid_str":"123456","type":31}]}`),
		[]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"123456","type":31}]} {}`),
		[]byte(`{"status_code":1,"notice_list_v2":[{"nid_str":"123456","type":31}]}`),
	} {
		if _, err := parseDouyinNoticeDetail(context.Background(), bad, refs); !errors.Is(err, ErrDouyinNoticeUnavailable) {
			t.Fatalf("unbound detail accepted: %s %v", bad, err)
		}
	}
	invalid := []byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"123456","type":31,"cid":"111","comment":{"cid":true}}]}`)
	items, err = parseDouyinNoticeDetail(context.Background(), invalid, refs)
	if err != nil || len(items) != 1 || items[0].CandidateFieldsValid || len(items[0].Candidates) != 0 {
		t.Fatalf("invalid candidates survived: %+v %v", items, err)
	}
}
