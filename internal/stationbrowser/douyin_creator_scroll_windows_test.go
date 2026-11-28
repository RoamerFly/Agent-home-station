//go:build windows

package stationbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreatorWorksNativeScrollLoadsNextResponse(t *testing.T) {
	if !Available() {
		t.Skip("Edge unavailable")
	}
	_, first := syntheticCreatorWork(t)
	first["author"] = map[string]string{"uid": "123456789012345678"}
	_, second := syntheticCreatorWork(t)
	second["aweme_id"] = "7391234567890123457"
	second["status"].(map[string]any)["aweme_id"] = second["aweme_id"]
	second["author"] = first["author"]
	var firstCalls, secondCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/creator-micro/content/manage":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><main id="works" style="width:500px;height:180px;overflow-y:auto">
<section id="cards"></section><span>加载中…</span></main><script>
const list=document.querySelector('#works'), cards=document.querySelector('#cards');
for(let i=0;i<12;i++){const card=document.createElement('article');card.style.height='60px';card.textContent='合成作品 '+i;cards.append(card)}
fetch('/janus/douyin/creator/pc/work_list?cursor=first');
let loading=false;
list.addEventListener('scroll',()=>{if(loading||list.scrollTop+list.clientHeight<list.scrollHeight-2)return;
loading=true;fetch('/janus/douyin/creator/pc/work_list?cursor=second')});
</script>`))
		case "/janus/douyin/creator/pc/work_list":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("cursor") == "second" {
				secondCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "has_more": false, "max_cursor": "2", "aweme_list": []any{second}})
			} else {
				firstCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "has_more": true, "max_cursor": "1", "aweme_list": []any{first}})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	read := func(input DouyinCreatorWorksRequest) (DouyinCreatorWorksPage, error) {
		return readDouyinCreatorWorks(t.Context(), filepath.Join(t.TempDir(), "profile"),
			server.URL+"/creator-micro/content/manage", input, func() error { return nil }, true)
	}
	firstPage, err := read(DouyinCreatorWorksRequest{})
	if err != nil || len(firstPage.Works) != 1 || firstPage.Works[0].ID != first["aweme_id"] || !firstPage.HasMore {
		t.Fatalf("first page = %+v, err = %v", firstPage, err)
	}
	secondPage, err := read(DouyinCreatorWorksRequest{Page: 1, PreviousAccount: firstPage.Account, PreviousPageKey: firstPage.PageKey})
	if err != nil || len(secondPage.Works) != 1 || secondPage.Works[0].ID != second["aweme_id"] || secondPage.HasMore ||
		firstCalls.Load() != 2 || secondCalls.Load() != 1 {
		t.Fatalf("scroll page = %+v, err = %v, requests = %d/%d", secondPage, err, firstCalls.Load(), secondCalls.Load())
	}
}

type creatorWorksAdvanceFixture struct {
	listener *creatorReceiptListener
	next     DouyinCreatorWorksPage
	advanced bool
}

func (f *creatorWorksAdvanceFixture) Call(_ context.Context, _, method string, params any, result any) error {
	if method == "Runtime.evaluate" {
		input, _ := params.(map[string]any)
		expression, _ := input["expression"].(string)
		clicked := strings.Contains(expression, `"works-next"`)
		if clicked && !f.advanced {
			f.listener.mu.Lock()
			f.listener.worksPage, f.listener.worksError = f.next, nil
			f.listener.worksSerial++
			f.listener.mu.Unlock()
			f.advanced = true
		}
		putReaderResult(result, map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"clicked": clicked}}})
	}
	return nil
}
func (f *creatorWorksAdvanceFixture) Events() <-chan cdpEvent { return nil }
func (f *creatorWorksAdvanceFixture) Close() error            { return nil }

func TestCreatorWorksRejectsOverlappingPageAndStalledCursor(t *testing.T) {
	const account = "synthetic-account"
	first := DouyinCreatorWorksPage{Account: account, PageKey: "first", PageIDs: []string{"100"}, MaxCursor: "10", HasMore: true, CoverageKnown: true}
	for _, tc := range []struct {
		name string
		next DouyinCreatorWorksPage
		want error
	}{
		{"continuous", DouyinCreatorWorksPage{Account: account, PageKey: "second", PageIDs: []string{"101"}, MaxCursor: "9", CoverageKnown: true}, nil},
		{"overlap", DouyinCreatorWorksPage{Account: account, PageKey: "second", PageIDs: []string{"100", "101"}, MaxCursor: "9", CoverageKnown: true}, ErrCreatorWorksChanged},
		{"stalled cursor", DouyinCreatorWorksPage{Account: account, PageKey: "second", PageIDs: []string{"101"}, MaxCursor: "10", HasMore: true, CoverageKnown: true}, ErrCreatorWorksChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener := &creatorReceiptListener{worksPage: first, worksSerial: 1}
			fixture := &creatorWorksAdvanceFixture{listener: listener, next: tc.next}
			var output DouyinCreatorWorksPage
			in := CreatorPublicationRequest{Submitted: true, worksRequest: &DouyinCreatorWorksRequest{
				Page: 1, PreviousAccount: account, PreviousPageKey: first.PageKey,
			}, worksPage: &output}
			err := awaitCreatorWorks(t.Context(), fixture, "session", 1, DouyinCreatorManageURL, in,
				func() error { return nil }, listener)
			if !errors.Is(err, tc.want) || tc.want == nil && (err != nil || output.PageKey != tc.next.PageKey) {
				t.Fatalf("page = %+v, err = %v", output, err)
			}
		})
	}
}

func TestCreatorWorksDistinguishesFirstAndNextMissingResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		page int
		want error
	}{
		{"first list", 0, ErrCreatorWorksListNotObserved},
		{"next list", 1, ErrCreatorWorksNextUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener := &creatorReceiptListener{}
			if tc.page == 1 {
				listener.worksPage = DouyinCreatorWorksPage{Account: "synthetic-account", PageKey: "first", PageIDs: []string{"100"}, HasMore: true, CoverageKnown: true}
				listener.worksSerial = 1
			}
			fixture := &creatorWorksAdvanceFixture{listener: listener, advanced: true}
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			var output DouyinCreatorWorksPage
			in := CreatorPublicationRequest{Submitted: true, worksRequest: &DouyinCreatorWorksRequest{
				Page: tc.page, PreviousAccount: "synthetic-account", PreviousPageKey: "first",
			}, worksPage: &output}
			err := awaitCreatorWorks(ctx, fixture, "session", 1, DouyinCreatorManageURL, in,
				func() error { return nil }, listener)
			if !errors.Is(err, tc.want) {
				t.Fatalf("missing page %d = %v, want %v", tc.page, err, tc.want)
			}
		})
	}
}
