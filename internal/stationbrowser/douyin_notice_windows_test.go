//go:build windows

package stationbrowser

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestDouyinNoticeNativeReadRequiresBoundAccountAndMarkReadConsent(t *testing.T) {
	if !Available() {
		t.Skip("Edge unavailable")
	}
	var listCalls, otherWrites atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/help":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data := url.PathEscape(`{"app":{"odin":{"user_id":"1234567890","user_is_auth":1}}}`)
			_, _ = w.Write([]byte(`<!doctype html><script type="application/json" id="RENDER_DATA">` + data + `</script>
<div data-e2e="notice-entry"><button data-e2e="something-button" onmouseenter="loadNotice()">通知</button></div>
<script>
function loadNotice() {
 const bad = new XMLHttpRequest(); bad.open('POST', '/aweme/v1/web/notice/del/'); bad.send();
 const req = new XMLHttpRequest(); req.open('GET', '/aweme/v1/web/notice/?is_new_notice=1&is_mark_read=1'); req.send();
}
</script>`))
		case douyinNoticePath:
			listCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"9182736450","type":2,"create_time":1700000000,"aweme_id":"7391234567890123456"}],"min_time":0,"max_time":1700000000,"has_more":1}`))
		default:
			if r.URL.Path != "/favicon.ico" {
				otherWrites.Add(1)
			}
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	read := func(input *DouyinNoticeRequest) (DouyinNoticePage, error) {
		return readDouyinNotice(t.Context(), filepath.Join(t.TempDir(), "profile"), server.URL+"/help", input, func() error { return nil }, true)
	}
	account, err := read(nil)
	if err != nil || len(account.Account) != 64 || listCalls.Load() != 0 {
		t.Fatalf("account-only probe unexpectedly read notifications: %+v %v, calls=%d", account, err, listCalls.Load())
	}
	if _, err := read(&DouyinNoticeRequest{Account: "other", AllowMarkRead: true}); !errors.Is(err, ErrDouyinNoticeAccountChanged) || listCalls.Load() != 0 {
		t.Fatalf("wrong account request: %v, calls=%d", err, listCalls.Load())
	}
	if _, err := read(&DouyinNoticeRequest{Account: account.Account}); !errors.Is(err, ErrDouyinNoticeMarksRead) || listCalls.Load() != 0 {
		t.Fatalf("mark-read attempted without authorization: %v, calls=%d", err, listCalls.Load())
	}
	page, err := read(&DouyinNoticeRequest{Account: account.Account, AllowMarkRead: true})
	if err != nil || page.Account != account.Account || len(page.Items) != 1 || page.Items[0].ID != "9182736450" || !page.HasMore || page.MaxTime != 1700000000 {
		t.Fatalf("bound inbox read: %+v %v", page, err)
	}
	if listCalls.Load() != 1 || otherWrites.Load() != 0 {
		t.Fatalf("unexpected requests: %d list, %d other", listCalls.Load(), otherWrites.Load())
	}
}

func TestDouyinNoticeListRequestRequiresExactOriginAndExplicitMarkFlag(t *testing.T) {
	const origin = "https://www.douyin.com"
	for _, raw := range []string{
		"https://creator.douyin.com" + douyinNoticePath + "?is_mark_read=1&is_new_notice=1",
		origin + douyinNoticePath + "?is_new_notice=1",
		origin + douyinNoticePath + "?is_new_notice=1&is_mark_read=0&is_mark_read=1",
		origin + douyinNoticePath + "detail/?is_new_notice=1&is_mark_read=0",
		origin + douyinNoticePath + "?is_new_notice=1&is_mark_read=2",
	} {
		if _, exact := noticeListRequest(raw, origin); exact {
			t.Errorf("admitted ambiguous route: %s", raw)
		}
	}
	for _, flag := range []string{"0", "1"} {
		mark, exact := noticeListRequest(origin+douyinNoticePath+"?is_new_notice=1&is_mark_read="+flag, origin)
		if !exact || mark != (flag == "1") {
			t.Errorf("mark flag %s: admitted=%v, marks=%v", flag, exact, mark)
		}
	}
}
