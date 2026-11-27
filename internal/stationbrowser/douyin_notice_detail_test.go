package stationbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
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

func TestDouyinNoticeDetailLikeAndEmptyResponseCannotSupplyCommentCandidate(t *testing.T) {
	refs := []noticeDetailRef{{id: "123456", requestType: 0}}
	like := []byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"123456","type":0,"create_time":1700000000,"digg":{"aweme":{"aweme_id":"200"}},"general_notice":{"message_extra":"{\"item_id\":\"200\",\"notice_type\":1}"}}]}`)
	items, err := parseDouyinNoticeDetail(t.Context(), like, refs)
	if err != nil || len(items) != 1 || items[0].WorkID != "" || len(items[0].Candidates) != 0 || !items[0].CandidateFieldsValid {
		t.Fatalf("like notice gained a comment candidate: %+v, %v", items, err)
	}
	if _, err := parseDouyinNoticeDetail(t.Context(), []byte(`{"status_code":0,"notice_list_v2":null}`), refs); !errors.Is(err, ErrDouyinNoticeUnavailable) {
		t.Fatalf("empty detail became a comment candidate: %v", err)
	}
}

func TestDouyinNestedCommentNoticeKeepsExactWorkAndTargetCandidate(t *testing.T) {
	// Synthetic shape of a web type-31 comment notice. No response text,
	// nickname, media URL or account-specific ID from the observed sample is kept.
	item := `{"nid_str":"123456","type":31,"create_time":1700000000,"comment":{"comment":{"cid":"400","aweme_id":"200","user":{"uid":"901","nickname":"private-name"},"text":"private-text"},"aweme":{"aweme_id":"200","desc":"private-caption"}}}`
	raw := []byte(`{"status_code":0,"has_more":0,"notice_list_v2":[` + item + `]}`)
	page, err := parseDouyinNoticePage(t.Context(), raw)
	if err != nil || len(page.Items) != 1 || page.Items[0].WorkID != "200" || len(page.Items[0].Candidates) != 1 ||
		page.Items[0].Candidates[0] != (DouyinNoticeCandidate{Path: "comment.comment.cid", ID: "400"}) {
		t.Fatalf("nested notice list = %+v, %v", page, err)
	}
	refs := []noticeDetailRef{{id: "123456", requestType: 31}}
	items, err := parseDouyinNoticeDetail(t.Context(), raw, refs)
	if err != nil || len(items) != 1 || items[0].WorkID != "200" || !items[0].CandidateFieldsValid ||
		len(items[0].Candidates) != 1 || items[0].Candidates[0] != (DouyinNoticeCandidate{Path: "comment.comment.cid", ID: "400"}) {
		t.Fatalf("nested notice detail = %+v, %v", items, err)
	}
	encoded, _ := json.Marshal(items)
	for _, private := range []string{"private-name", "private-text", "private-caption"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private notification field retained: %s", private)
		}
	}
	conflict := strings.Replace(item, `"aweme":{"aweme_id":"200"`, `"aweme":{"aweme_id":"201"`, 1)
	bad := []byte(`{"status_code":0,"has_more":0,"notice_list_v2":[` + conflict + `]}`)
	badPage, err := parseDouyinNoticePage(t.Context(), bad)
	if err != nil || len(badPage.Items) != 1 || badPage.Items[0].WorkID != "" || len(badPage.Items[0].Candidates) != 0 {
		t.Fatalf("conflicting nested work survived list parsing: %+v, %v", badPage, err)
	}
	badItems, err := parseDouyinNoticeDetail(t.Context(), bad, refs)
	if err != nil || len(badItems) != 1 || badItems[0].WorkID != "" || badItems[0].CandidateFieldsValid {
		t.Fatalf("conflicting nested work survived detail parsing: %+v, %v", badItems, err)
	}
}

func TestDouyinNestedChildNoticeKeepsParentIDUnclassified(t *testing.T) {
	// A child notice has a target CID but no reply_id. The type-11 parent_id
	// is only a candidate for the root reply-list request; the exact target
	// row must still confirm its reply_id before any relation is trusted.
	raw := []byte(`{"status_code":0,"has_more":0,"notice_list_v2":[{"nid_str":"123456","type":31,"aweme_id":"200","comment":{"comment":{"cid":"500","aweme_id":"200","text":"private-child"},"aweme":{"aweme_id":"200"},"comment_type":11,"parent_id":"400","reply_comment":{"text":"private-parent"},"level1_comment":{"text":"private-root"}}}]}`)
	want := []DouyinNoticeCandidate{{Path: "comment.parent_id", ID: "400"}, {Path: "comment.comment.cid", ID: "500"}}
	page, err := parseDouyinNoticePage(t.Context(), raw)
	if err != nil || len(page.Items) != 1 || page.Items[0].WorkID != "200" ||
		!reflect.DeepEqual(page.Items[0].Candidates, want) {
		t.Fatalf("child notice list lost unclassified candidates: %+v, %v", page, err)
	}
	items, err := parseDouyinNoticeDetail(t.Context(), raw, []noticeDetailRef{{id: "123456", requestType: 31}})
	if err != nil || len(items) != 1 || items[0].WorkID != "200" || !items[0].CandidateFieldsValid ||
		!reflect.DeepEqual(items[0].Candidates, want) {
		t.Fatalf("child notice detail lost unclassified candidates: %+v, %v", items, err)
	}
	encoded, _ := json.Marshal(items)
	for _, private := range []string{"private-child", "private-parent", "private-root"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private child notice text retained: %s", private)
		}
	}
	main := strings.Replace(string(raw), `"comment_type":11`, `"comment_type":1`, 1)
	mainItems, err := parseDouyinNoticeDetail(t.Context(), []byte(main), []noticeDetailRef{{id: "123456", requestType: 31}})
	if err != nil || len(mainItems) != 1 || len(mainItems[0].Candidates) != 1 ||
		mainItems[0].Candidates[0] != (DouyinNoticeCandidate{Path: "comment.comment.cid", ID: "500"}) {
		t.Fatalf("main-comment notice treated parent_id as a root: %+v, %v", mainItems, err)
	}
	bad := strings.Replace(string(raw), `"parent_id":"400"`, `"parent_id":"invalid"`, 1)
	badItems, err := parseDouyinNoticeDetail(t.Context(), []byte(bad), []noticeDetailRef{{id: "123456", requestType: 31}})
	if err != nil || len(badItems) != 1 || badItems[0].CandidateFieldsValid || len(badItems[0].Candidates) != 0 {
		t.Fatalf("invalid child parent_id survived normalization: %+v, %v", badItems, err)
	}
}
