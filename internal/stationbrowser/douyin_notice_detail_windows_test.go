//go:build windows

package stationbrowser

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestDouyinNoticeNaturallyIssuedDetailIsBoundAndNormalized(t *testing.T) {
	if !Available() {
		t.Skip("Edge unavailable")
	}
	var listCalls, detailCalls, unexpected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/help":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data := url.PathEscape(`{"app":{"odin":{"user_id":"1234567890","user_is_auth":1}}}`)
			_, _ = w.Write([]byte(`<!doctype html><script type="application/json" id="RENDER_DATA">` + data + `</script>
<div data-e2e="notice-entry"><button data-e2e="something-button" onmouseenter="loadNotice()">通知</button></div>
<script>
function loadNotice() {
 const req = new XMLHttpRequest();
 req.open('GET', '/aweme/v1/web/notice/?is_new_notice=1&is_mark_read=1&notice_group=2&count=10&min_time=0&max_time=0');
 req.onload = () => {
   const panel = document.createElement('section'); panel.dataset.e2e = 'listDlgTest-container';
   panel.innerHTML = '<div class="sKMx4DKd">合成用户评论了你的作品</div>'; document.body.append(panel);
   const ids = encodeURIComponent(JSON.stringify([{notice_id_str:'9182736450',type:0}]));
   const detail = new XMLHttpRequest(); detail.open('GET', '/aweme/v1/web/notice/detail/?aid=6383&id_list=' + ids); detail.send();
   const unknown = encodeURIComponent(JSON.stringify([{notice_id_str:'1111111111',type:0}]));
   const denied = new XMLHttpRequest(); denied.open('GET', '/aweme/v1/web/notice/detail/?aid=6383&id_list=' + unknown); denied.send();
 };
 req.send();
}
</script>`))
		case douyinNoticePath:
			listCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"9182736450","type":31,"create_time":1700000000,"aweme_id":"7391234567890123456"}],"min_time":0,"max_time":0,"has_more":0}`))
		case douyinNoticeDetailPath:
			detailCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"9182736450","type":31,"create_time":1700000000,"aweme_id":"7391234567890123456","cid":"111","comment":{"cid":"222"}}]}`))
		default:
			if r.URL.Path != "/favicon.ico" {
				unexpected.Add(1)
			}
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	profile := filepath.Join(t.TempDir(), "profile")
	account, err := readDouyinNotice(t.Context(), profile, server.URL+"/help", nil, func() error { return nil }, true)
	if err != nil || account.Account == "" || listCalls.Load() != 0 || detailCalls.Load() != 0 {
		t.Fatalf("account probe caused notice calls: %+v %v", account, err)
	}
	page, err := readDouyinNotice(t.Context(), profile, server.URL+"/help", &DouyinNoticeRequest{Account: account.Account, AllowMarkRead: true}, func() error { return nil }, true)
	if err != nil || len(page.Items) != 1 || len(page.Details) != 1 || page.Details[0].NoticeID != page.Items[0].ID ||
		len(page.Details[0].Candidates) != 2 || page.DetailProgress != "" || listCalls.Load() != 1 || detailCalls.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("natural detail was not bound: %+v %v; requests=%d/%d/%d", page, err, listCalls.Load(), detailCalls.Load(), unexpected.Load())
	}
}

func TestDouyinNoticeDetailEvidenceProgressAndConflicts(t *testing.T) {
	fence := &noticeRequestFence{}
	items := []DouyinNotice{{ID: "123456"}}
	if details, progress := fence.snapshotDetails(items); len(details) != 0 || progress != "not_observed" {
		t.Fatalf("missing natural detail was hidden: %+v %s", details, progress)
	}
	fence.recordDetails([]DouyinNoticeDetail{{NoticeID: "123456", Candidates: []DouyinNoticeCandidate{{Path: "cid", ID: "111"}}}})
	if details, progress := fence.snapshotDetails(items); len(details) != 1 || progress != "" {
		t.Fatalf("detail not retained: %+v %s", details, progress)
	}
	fence.recordDetails([]DouyinNoticeDetail{{NoticeID: "123456", Candidates: []DouyinNoticeCandidate{{Path: "cid", ID: "222"}}}})
	if details, progress := fence.snapshotDetails(items); len(details) != 0 || progress != "conflict" {
		t.Fatalf("conflicting detail retained: %+v %s", details, progress)
	}
}
