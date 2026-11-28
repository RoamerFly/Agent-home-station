//go:build windows

package stationbrowser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLocalDouyinCommentsPagingAndSourceFields(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	s := douyinCommentsFixture(t)
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	profile := t.TempDir() + "\\profile"
	first, err := readDouyinComments(ctx, profile, s.URL+"/video/123", DouyinCommentsRequest{VideoID: "123", Limit: 2}, func() error { return nil }, true)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Comments) != 2 || first.Comments[0].ID != "101" || first.Comments[0].AuthorID != "sec_alpha" || first.Comments[0].Text != "第一条" || first.Comments[0].CreatedAt != "2026-01-02T03:04:05.000Z" || first.Complete || first.NextCursor != "102" {
		t.Fatalf("first page=%+v", first)
	}
	second, err := readDouyinComments(ctx, profile, s.URL+"/video/123", DouyinCommentsRequest{VideoID: "123", Cursor: first.NextCursor, Limit: 2}, func() error { return nil }, true)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Comments) != 2 || second.Comments[0].ID != "103" || second.Comments[1].ID != "104" || !second.Complete || second.NextCursor != "" {
		t.Fatalf("second page=%+v", second)
	}
	if first.Comments[1].CreatedAt != "" {
		t.Fatal("relative date was guessed", first.Comments[1].CreatedAt)
	}
	// This anchor is not in the initial two cards. A resumed read must scroll
	// to find it instead of declaring the original cursor expired immediately.
	last, err := readDouyinComments(ctx, profile, s.URL+"/video/123", DouyinCommentsRequest{VideoID: "123", Cursor: "103", Limit: 1}, func() error { return nil }, true)
	if err != nil || len(last.Comments) != 1 || last.Comments[0].ID != "104" || !last.Complete {
		t.Fatalf("later anchor: %#v %v", last, err)
	}
}

func TestLocalDouyinCommentsTooltipIDsStayWithinCommentCards(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/invalid":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title>
<div id="merge-all-comment-container"><div data-e2e="comment-item"><div id="tooltip_323456789012345678"></div>
<span data-e2e="comment-user-name" data-sec-uid="sec_bad">无效</span><p data-e2e="comment-content">无效编号</p>
<a href="/video/4234567890123456789">作品链接</a></div><div data-e2e="comments-end">结束</div></div>`))
		case "/multiple":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><div id="merge-all-comment-container">
<div data-e2e="comment-item"><span data-e2e="comment-user-name" data-sec-uid="sec_bad">无效</span><p data-e2e="comment-content">多个编号</p>
<div id="tooltip_3234567890123456789"></div><div id="tooltip_4234567890123456789"></div></div></div>`))
		case "/missing":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><div data-e2e="comment-item" data-comment-id="123"><span data-e2e="comment-user-name" data-sec-uid="sec_old">旧页面</span><p data-e2e="comment-content">无原生容器</p></div>`))
		case "/bad-author":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><div id="merge-all-comment-container"><div data-e2e="comment-item">
<div id="tooltip_3234567890123456789"></div><div class="comment-item-info-wrap"><a href="//www.douyin.com/user/bad%2Fid">无效作者</a><a href="//www.douyin.com/user/sec_target">被回复者</a></div>
<div class="FduGc_lz">正文</div></div></div>`))
		case "/ambiguous-body":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><div id="merge-all-comment-container"><div data-e2e="comment-item">
<div id="tooltip_3234567890123456789"></div><div class="comment-item-info-wrap"><a href="//www.douyin.com/user/sec_author">作者</a></div>
<div class="FduGc_lz">正文甲</div><div class="FduGc_lz">正文乙</div></div></div>`))
		case "/direct", "/scroll":
			prefix := ""
			suffix := ""
			if r.URL.Path == "/scroll" {
				prefix = `<style>.route-scroll-container{height:70px;overflow-y:auto}</style><div id="scroll-root" class="route-scroll-container">`
				suffix = `<div style="height:400px">填充</div></div>`
			}
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title>` + prefix + `<div data-e2e="comment-list" class="comment-mainContent">
<div data-e2e="comment-item"><div class="comment-item-info-wrap"><a href="//www.douyin.com/user/sec_direct">作者</a></div><div class="FduGc_lz">直达页评论</div>
<div><div><div id="tooltip_3234567890123456789"></div></div></div></div></div>` + suffix))
		case "/both":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><div id="merge-all-comment-container"></div><div data-e2e="comment-list" class="comment-mainContent"></div>`))
		case "/native-end":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><div id="merge-all-comment-container">
<div data-e2e="comment-list" class="comment-mainContent">
<div data-e2e="comment-item"><div id="tooltip_3234567890123456789"></div><div class="comment-item-info-wrap"><a href="//www.douyin.com/user/sec_native">作者</a></div><div class="FduGc_lz">真实结构评论</div></div>
<div class="Fk1OBkgC">暂时没有更多评论</div></div></div>`))
		case "/native-end-outside":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><div id="merge-all-comment-container">
<div data-e2e="comment-list" class="comment-mainContent"><div data-e2e="comment-item"><div id="tooltip_3234567890123456789"></div><div class="comment-item-info-wrap"><a href="//www.douyin.com/user/sec_native">作者</a></div><div class="FduGc_lz">真实结构评论</div></div></div>
<div class="Fk1OBkgC">暂时没有更多评论</div></div>`))
		case "/native-empty":
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><div id="merge-all-comment-container">
<div data-e2e="comment-list" class="comment-mainContent"><div class="Fk1OBkgC">暂时没有更多评论</div></div></div>`))
		default:
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title>
<div data-e2e="comment-item"><div id="tooltip_9234567890123456789"></div><span data-sec-uid="sec_outside">外部</span><p data-e2e="comment-content">容器外</p></div>
<div id="merge-all-comment-container">
  <div data-e2e="comment-item"><div class="comment-item-info-wrap"><a href="//www.douyin.com/user/sec_root">甲</a></div><div class="FduGc_lz">主评论</div>
    <div><div><div><div><div id="tooltip_1234567890123456789"></div></div></div></div></div>
    <div class="replyContainer"><div data-e2e="comment-item"><div class="comment-item-info-wrap"><a href="//www.douyin.com/user/sec_child">乙</a><a href="//www.douyin.com/user/sec_root">被回复者</a></div><div class="FduGc_lz">子评论</div>
      <div><div><div><div><div id="tooltip_2234567890123456789"></div></div></div></div></div>
    </div></div>
  </div>
  <div data-e2e="comments-end">结束</div>
</div>`))
		}
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	profile := t.TempDir() + "\\profile"
	page, err := readDouyinComments(ctx, profile, s.URL+"/video/123", DouyinCommentsRequest{VideoID: "123", Limit: 10}, func() error { return nil }, true)
	if err != nil || !page.Complete || len(page.Comments) != 2 ||
		page.Comments[0].ID != "1234567890123456789" || page.Comments[0].AuthorID != "sec_root" ||
		page.Comments[0].Text != "主评论" || page.Comments[1].ID != "2234567890123456789" ||
		page.Comments[1].AuthorID != "sec_child" || page.Comments[1].Text != "子评论" {
		t.Fatalf("tooltip comment cards: %+v %v", page, err)
	}
	probe, err := openCDP(ctx, t.TempDir()+"\\probe-profile")
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	var created struct{ TargetID string }
	if err := probe.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil || created.TargetID == "" {
		t.Fatalf("create synthetic comment tab: %v", err)
	}
	var attached struct{ SessionID string }
	if err := probe.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, &attached); err != nil || attached.SessionID == "" {
		t.Fatalf("attach synthetic comment tab: %v", err)
	}
	if err := probe.Call(ctx, attached.SessionID, "Page.enable", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	stateAt := func(path string, localFixture bool) renderedDouyinComments {
		t.Helper()
		target := s.URL + path
		var nav struct{ ErrorText string }
		if err := probe.Call(ctx, attached.SessionID, "Page.navigate", map[string]any{"url": target}, &nav); err != nil || nav.ErrorText != "" {
			t.Fatalf("navigate synthetic comment tab: %v %s", err, nav.ErrorText)
		}
		for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
			state, err := renderedDouyinCommentsPage(ctx, probe, attached.SessionID, target, localFixture)
			if err == nil && state.Ready && state.URL == target {
				return state
			}
		}
		t.Fatalf("synthetic comment tab did not load %s", path)
		return renderedDouyinComments{}
	}
	native := stateAt("/video/123", false)
	if native.Invalid || len(native.Comments) != 2 || native.Comments[0].ID != page.Comments[0].ID || native.Comments[1].ID != page.Comments[1].ID {
		t.Fatalf("production parser lost nested comment cards: %+v", native)
	}
	direct := stateAt("/direct", false)
	if direct.Invalid || len(direct.Comments) != 1 || direct.Comments[0].ID != "3234567890123456789" || direct.Comments[0].AuthorID != "sec_direct" {
		t.Fatalf("production parser lost direct video comment list: %+v", direct)
	}
	nativeEnd := stateAt("/native-end", false)
	if nativeEnd.Invalid || !nativeEnd.End || len(nativeEnd.Comments) != 1 || nativeEnd.Comments[0].AuthorID != "sec_native" {
		t.Fatalf("production parser lost nested native list or end marker: %+v", nativeEnd)
	}
	if outside := stateAt("/native-end-outside", false); outside.Invalid || outside.End || len(outside.Comments) != 1 {
		t.Fatalf("end marker outside the exact list was accepted: %+v", outside)
	}
	if empty := stateAt("/native-empty", false); empty.Invalid || !empty.Loaded || !empty.Empty || !empty.End || len(empty.Comments) != 0 {
		t.Fatalf("native empty list was not recognized: %+v", empty)
	}
	for _, path := range []string{"/invalid", "/multiple", "/bad-author", "/ambiguous-body"} {
		state := stateAt(path, false)
		if !state.Invalid || len(state.Comments) != 0 {
			t.Fatalf("ambiguous tooltip or linked video ID accepted: %s %+v", path, state)
		}
	}
	missing := stateAt("/missing", false)
	if !missing.Invalid || len(missing.Comments) != 0 {
		t.Fatalf("production read accepted missing native container: %+v", missing)
	}
	both := stateAt("/both", false)
	if !both.Invalid || len(both.Comments) != 0 {
		t.Fatalf("production read accepted multiple native containers: %+v", both)
	}
	if state := stateAt("/scroll", false); state.Invalid || len(state.Comments) != 1 {
		t.Fatalf("synthetic scroll list unavailable: %+v", state)
	}
	if err := scrollDouyinComments(ctx, probe, attached.SessionID); err != nil {
		t.Fatal(err)
	}
	var scrolled struct {
		Result struct {
			Value struct{ Parent, List float64 }
		}
	}
	if err := probe.Call(ctx, attached.SessionID, "Runtime.evaluate", map[string]any{"expression": `({parent:document.querySelector('#scroll-root').scrollTop,list:document.querySelector('[data-e2e="comment-list"]').scrollTop})`, "returnByValue": true}, &scrolled); err != nil || scrolled.Result.Value.Parent <= 0 || scrolled.Result.Value.List != 0 {
		t.Fatalf("comment list did not scroll its real ancestor: %+v %v", scrolled, err)
	}
}

func TestLocalDouyinCommentsEmptyLoginCursorRevocationAndNavigation(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	s := douyinCommentsFixture(t)
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	profile := t.TempDir() + "\\profile"
	empty, err := readDouyinComments(ctx, profile, s.URL+"/empty", DouyinCommentsRequest{Limit: 0}, func() error { return nil }, true)
	if err != nil || !empty.Complete || len(empty.Comments) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	_, err = readDouyinComments(ctx, profile, s.URL+"/video/123", DouyinCommentsRequest{Cursor: "999", Limit: 1}, func() error { return nil }, true)
	if !errors.Is(err, ErrDouyinCommentsCursorExpired) {
		t.Fatalf("expired cursor=%v", err)
	}
	_, err = readDouyinComments(ctx, profile, s.URL+"/login", DouyinCommentsRequest{Limit: 1}, func() error { return nil }, true)
	if !errors.Is(err, ErrDouyinWebLoginRequired) {
		t.Fatalf("login=%v", err)
	}
	revoked := errors.New("authorization revoked")
	calls := 0
	_, err = readDouyinComments(ctx, profile, s.URL+"/video/123", DouyinCommentsRequest{Limit: 1}, func() error {
		calls++
		if calls > 1 {
			return revoked
		}
		return nil
	}, true)
	if !errors.Is(err, revoked) {
		t.Fatalf("revocation=%v", err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("other")) }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer redirect.Close()
	_, err = readDouyinComments(ctx, profile, redirect.URL+"/video/123", DouyinCommentsRequest{Limit: 1}, func() error { return nil }, true)
	if !errors.Is(err, ErrDouyinCommentsUnavailable) {
		t.Fatalf("redirect=%v", err)
	}
}

func TestDouyinCommentsLargeLimitIsResumableNotClamped(t *testing.T) {
	if got, err := normalizedDouyinCommentsLimit(10000); err != nil || got != 10000 {
		t.Fatalf("large limit=%d %v", got, err)
	}
	comments := make([]DouyinComment, 0, 201)
	for i := 1; i <= 201; i++ {
		comments = append(comments, DouyinComment{ID: fmtCommentID(i), AuthorID: "sec_a", Text: "x"})
	}
	page, err := douyinCommentsAfterCursor(renderedDouyinComments{Comments: comments, End: true}, "", 10000)
	if err != nil || page.Complete || len(page.Comments) != maxDouyinCommentsBatch || page.NextCursor != "200" {
		t.Fatalf("large page=%+v err=%v", page, err)
	}
}

func fmtCommentID(i int) string { return fmt.Sprintf("%d", i) }

func douyinCommentsFixture(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/empty":
			_, _ = w.Write([]byte(`<title>empty</title><div data-e2e="comment-empty">暂无评论</div>`))
		case "/login":
			_, _ = w.Write([]byte(`<title>login</title><form action="/login"><input type="password"></form>`))
		default:
			_, _ = w.Write([]byte(`<!doctype html><title>comments</title><main id="comments"></main><script>
const data=[['101','sec_alpha','甲','第一条','2026-01-02T03:04:05Z'],['102','sec_beta','乙','第二条','几分钟前'],['103','sec_gamma','丙','第三条','2026-01-02T03:06:05Z'],['104','sec_delta','丁','第四条','2026-01-02T03:07:05Z']];let n=0;
function add(k){while(n<data.length&&k-->0){let x=data[n++],e=document.createElement('article');e.dataset.e2e='comment-item';e.dataset.commentId=x[0];e.dataset.authorId=x[1];e.innerHTML='<span data-e2e="comment-user-name" data-sec-uid="'+x[1]+'">'+x[2]+'</span><p data-e2e="comment-content">'+x[3]+'</p><time datetime="'+x[4]+'">'+x[4]+'</time>';comments.append(e)}if(n===data.length){let e=document.createElement('i');e.dataset.e2e='comments-end';comments.append(e)}}add(2);window.scrollTo=()=>add(2);
</script>`))
		}
	}))
}
