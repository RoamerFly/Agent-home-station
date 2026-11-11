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
	var changed, scroll, stalled, unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/help":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data := url.PathEscape(`{"app":{"odin":{"user_id":"1234567890","user_is_auth":1}}}`)
			mode := "false"
			if scroll.Load() {
				mode = "true"
			}
			missingControl := "false"
			if unavailable.Load() {
				missingControl = "true"
			}
			_, _ = w.Write([]byte(`<!doctype html><script type="application/json" id="RENDER_DATA">` + data + `</script>
<div data-e2e="notice-entry"><button data-e2e="something-button" onmouseenter="loadNotice()">通知</button></div>
<script>
function loadNotice(max = 0) {
 if (typeof max !== 'number') max = 0;
 const bad = new XMLHttpRequest(); bad.open('POST', '/aweme/v1/web/notice/del/'); bad.send();
 const req = new XMLHttpRequest();
 req.open('GET', '/aweme/v1/web/notice/?is_new_notice=1&is_mark_read=1&notice_group=2&count=10&min_time=0&max_time=' + max);
 req.onload = () => {
   if (req.status !== 200) return;
   let panel = document.querySelector('[data-e2e="listDlgTest-container"]');
   if (!panel) {
     panel = document.createElement('section');
     panel.dataset.e2e = 'listDlgTest-container';
     document.body.append(panel);
   }
   if (max === 0) {
     panel.innerHTML = '<h2>互动消息</h2><div class="sKMx4DKd"><p>合成用户评论了你的作品</p><span class="RQOLsL3h" onclick="loadMore()">点击加载更多</span><p hidden>隐藏字段</p></div>';
     if (` + mode + `) {
       const list = panel.querySelector('.sKMx4DKd');
       list.style = 'height:80px;overflow:auto';
       list.innerHTML = '<p style="height:500px">合成用户评论了你的作品</p>';
       list.onscroll = () => { list.onscroll = null; loadMore(); };
     }
     if (` + missingControl + `) panel.querySelector('.sKMx4DKd').innerHTML = '<p>合成用户评论了你的作品</p>';
   } else {
     panel.querySelector('.sKMx4DKd').innerHTML = '<p>合成用户赞了你的作品</p>';
   }
 };
 req.send();
}
function loadMore() {
 const bad = new XMLHttpRequest();
 bad.open('GET', '/aweme/v1/web/notice/?is_new_notice=1&is_mark_read=1&notice_group=3&count=10&min_time=0&max_time=1700000000');
 bad.send();
 const wrongCursor = new XMLHttpRequest();
 wrongCursor.open('GET', '/aweme/v1/web/notice/?is_new_notice=1&is_mark_read=1&notice_group=2&count=10&min_time=0&max_time=1700000001');
 wrongCursor.send();
 loadNotice(1700000000);
}
</script>`))
		case douyinNoticePath:
			listCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			switch max := r.URL.Query().Get("max_time"); {
			case r.URL.Query().Get("notice_group") != "2":
				otherWrites.Add(1)
				http.Error(w, "wrong scope", http.StatusForbidden)
			case max == "0" && changed.Load():
				_, _ = w.Write([]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"9182736499","type":2}],"min_time":0,"max_time":1700000000,"has_more":1}`))
			case max == "0":
				_, _ = w.Write([]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"9182736450","type":2,"create_time":1700000000,"aweme_id":"7391234567890123456"}],"min_time":0,"max_time":1700000000,"has_more":1}`))
			case max == "1700000000":
				if stalled.Load() {
					_, _ = w.Write([]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"9182736449","type":3}],"min_time":0,"max_time":1700000000,"has_more":1}`))
				} else {
					_, _ = w.Write([]byte(`{"status_code":0,"notice_list_v2":[{"nid_str":"9182736450","type":2},{"nid_str":"9182736449","type":3}],"min_time":0,"max_time":1699999999,"has_more":0}`))
				}
			default:
				otherWrites.Add(1)
				http.Error(w, "wrong cursor", http.StatusForbidden)
			}
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
	if err != nil || page.Account != account.Account || len(page.Items) != 1 || page.Items[0].ID != "9182736450" || !page.HasMore || page.MaxTime != 1700000000 ||
		page.RenderedText != "合成用户评论了你的作品" {
		t.Fatalf("bound inbox read: %+v %v", page, err)
	}
	if listCalls.Load() != 1 || otherWrites.Load() != 0 {
		t.Fatalf("unexpected requests: %d list, %d other", listCalls.Load(), otherWrites.Load())
	}
	resumed, err := read(&DouyinNoticeRequest{Account: account.Account, AllowMarkRead: true, After: page.Next})
	if err != nil || resumed.HasMore || resumed.PagesRead != 1 || len(resumed.Items) != 1 || resumed.Items[0].ID != "9182736449" ||
		resumed.RenderedText != "合成用户赞了你的作品" || listCalls.Load() != 3 || otherWrites.Load() != 0 {
		t.Fatalf("resume did not preserve cursor and deduplicate: %+v %v calls=%d others=%d", resumed, err, listCalls.Load(), otherWrites.Load())
	}
	changed.Store(true)
	if _, err := read(&DouyinNoticeRequest{Account: account.Account, AllowMarkRead: true, After: page.Next}); !errors.Is(err, ErrDouyinNoticeCursorChanged) || listCalls.Load() != 4 || otherWrites.Load() != 0 {
		t.Fatalf("changed checkpoint reached another page: %v, calls=%d", err, listCalls.Load())
	}
	changed.Store(false)
	for _, scrollMode := range []bool{false, true} {
		scroll.Store(scrollMode)
		before := listCalls.Load()
		bulk, err := read(&DouyinNoticeRequest{Account: account.Account, AllowMarkRead: true, Pages: 2})
		if err != nil || bulk.PagesRead != 2 || bulk.HasMore || len(bulk.Items) != 2 || listCalls.Load() != before+2 || otherWrites.Load() != 0 {
			t.Fatalf("bulk/scroll=%v: %+v %v calls=%d", scrollMode, bulk, err, listCalls.Load())
		}
	}
	stalled.Store(true)
	before := listCalls.Load()
	partial, err := read(&DouyinNoticeRequest{Account: account.Account, AllowMarkRead: true, Pages: 3})
	if err != nil || partial.PagesRead != 2 || !partial.HasMore || partial.Progress != "cursor_stalled" || partial.Next.Pages != 0 || listCalls.Load() != before+2 {
		t.Fatalf("stalled cursor was silently advanced: %+v %v", partial, err)
	}
	stalled.Store(false)
	unavailable.Store(true)
	before = listCalls.Load()
	partial, err = read(&DouyinNoticeRequest{Account: account.Account, AllowMarkRead: true, Pages: 2})
	if err != nil || partial.PagesRead != 1 || !partial.HasMore || partial.Progress != "next_unavailable" || partial.Next != page.Next || listCalls.Load() != before+1 {
		t.Fatalf("missing native control lost verified progress: %+v %v", partial, err)
	}
	unavailable.Store(false)
	before = listCalls.Load()
	denied := errors.New("synthetic authorization revoked")
	_, err = readDouyinNotice(t.Context(), filepath.Join(t.TempDir(), "profile"), server.URL+"/help",
		&DouyinNoticeRequest{Account: account.Account, AllowMarkRead: true, Pages: 2}, func() error {
			if listCalls.Load() > before {
				return denied
			}
			return nil
		}, true)
	if !errors.Is(err, denied) || listCalls.Load() != before+1 || otherWrites.Load() != 0 {
		t.Fatalf("revocation admitted next request: %v calls=%d", err, listCalls.Load())
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
		if _, _, exact := noticeListRequest(raw+"&notice_group=2&count=10&min_time=0&max_time=0", origin); exact {
			t.Errorf("admitted ambiguous route: %s", raw)
		}
	}
	for _, flag := range []string{"0", "1"} {
		_, mark, exact := noticeListRequest(origin+douyinNoticePath+"?is_new_notice=1&notice_group=2&count=10&min_time=0&max_time=0&is_mark_read="+flag, origin)
		if !exact || mark != (flag == "1") {
			t.Errorf("mark flag %s: admitted=%v, marks=%v", flag, exact, mark)
		}
	}
}
