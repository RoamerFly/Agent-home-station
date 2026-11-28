//go:build windows

package stationbrowser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const commentLikeCard = `<article data-e2e="comment-item" data-comment-id="456"><a data-sec-uid="author_1">作者</a><p data-e2e="comment-content">评论正文</p><button data-e2e="comment-like-button" aria-pressed="false" onclick="this.setAttribute('aria-pressed','true');localStorage.liked='1';fetch('/aweme/v1/web/comment/digg/?cid=456&amp;aweme_id=123&amp;digg_type=1',{method:'POST'})">赞</button></article>`

func commentLikeFixture(t *testing.T, html string, clicks *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == douyinCommentDiggPath && r.Method == http.MethodPost {
			clicks.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("reject") == "1" {
				_, _ = fmt.Fprint(w, `{"status_code":99,"msg":"rejected"}`)
			} else {
				_, _ = fmt.Fprint(w, `{"status_code":0,"msg":"","extra":{}}`)
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>comment like</title>`, html,
			`<script>if(localStorage.liked==='1')document.querySelector('[data-e2e="comment-like-button"]')?.setAttribute('aria-pressed','true')</script>`)
	}))
}

func TestExactDouyinCommentDiggRequest(t *testing.T) {
	origin := "https://www.douyin.com"
	base := origin + douyinCommentDiggPath + "?cid=456&aweme_id=123&digg_type=1&msToken=redacted"
	for _, tc := range []struct {
		url, body string
		has       bool
		want      bool
	}{
		{base, "", false, true},
		{base, "cid=456&aweme_id=123&digg_type=1", true, true},
		{base, "cid=other", true, false},
		{base, "", true, false},
		{origin + douyinCommentDiggPath + "?cid=457&aweme_id=123&digg_type=1", "", false, false},
		{origin + douyinCommentDiggPath + "?cid=456&aweme_id=123&digg_type=0", "", false, false},
		{base + "&cid=456", "", false, false},
		{"https://evil.example" + douyinCommentDiggPath + "?cid=456&aweme_id=123&digg_type=1", "", false, false},
	} {
		if got := exactDouyinCommentDiggRequest(tc.url, tc.body, tc.has, origin, "123", "456"); got != tc.want {
			t.Fatalf("request match=%t want=%t", got, tc.want)
		}
	}
}

func commentLikeRequest() DouyinCommentLikeRequest {
	return DouyinCommentLikeRequest{VideoID: "123", CommentID: "456", ExpectedText: "评论正文", ExpectedAuthorID: "author_1", BeforeSubmit: func(context.Context) error { return nil }}
}

func TestLocalDouyinCommentLikeSingleClickAndRecovery(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	var clicks atomic.Int32
	s := commentLikeFixture(t, commentLikeCard, &clicks)
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	profile := t.TempDir() + "\\profile"
	before := 0
	req := commentLikeRequest()
	req.BeforeSubmit = func(context.Context) error { before++; return nil }
	got, err := runDouyinCommentLike(ctx, profile, s.URL+"/video/123", req, func() error { return nil }, true)
	if err != nil || got.State != DouyinInteractionApplied || !got.Submitted || before != 1 {
		t.Fatalf("single click=%+v %v before=%d", got, err, before)
	}
	for until := time.Now().Add(time.Second); clicks.Load() == 0 && time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
	}
	if clicks.Load() != 1 {
		t.Fatalf("clicks=%d", clicks.Load())
	}
	req.Submitted, req.BeforeSubmit = true, nil
	got, err = runDouyinCommentLike(ctx, profile, s.URL+"/video/123", req, func() error { return nil }, true)
	if err != nil || got.State != DouyinInteractionApplied || got.Code != "already_applied" || clicks.Load() != 1 {
		t.Fatalf("recovery=%+v %v clicks=%d", got, err, clicks.Load())
	}
}

func TestLocalDouyinCommentLikeRevealsExactChild(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	var clicks atomic.Int32
	html := `<div id="merge-all-comment-container"><article data-e2e="comment-item"><div id="tooltip_789"></div>` +
		`<button data-e2e="comment-replies-more" onclick="document.querySelector('#child-thread').style.display='block';this.remove()">展开回复</button>` +
		`<div id="child-thread" class="replyContainer" style="display:none">` + commentLikeCard + `</div></article></div>`
	s := commentLikeFixture(t, html, &clicks)
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	req := commentLikeRequest()
	req.RootCommentID = "789"
	got, err := runDouyinCommentLike(ctx, t.TempDir()+"\\profile", s.URL+"/video/123", req, func() error { return nil }, true)
	if err != nil || got.State != DouyinInteractionApplied || clicks.Load() != 1 {
		t.Fatalf("child like=%+v %v clicks=%d", got, err, clicks.Load())
	}
}

func TestLocalDouyinCommentLikeFailsClosedOnUnverifiedControls(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	cases := map[string]string{
		"unknown state": `<article data-e2e="comment-item" data-comment-id="456"><a data-sec-uid="author_1">作者</a><p data-e2e="comment-content">评论正文</p><button data-e2e="comment-like-button">赞</button></article>`,
		"ambiguous":     commentLikeCard + `<article data-e2e="comment-item" data-comment-id="456"><a data-sec-uid="author_1">作者</a><p data-e2e="comment-content">评论正文</p><button data-e2e="comment-like-button" aria-pressed="false">赞</button></article>`,
		"video control": `<article data-e2e="comment-item" data-comment-id="456"><a data-sec-uid="author_1">作者</a><p data-e2e="comment-content">评论正文</p><button data-e2e="like-button" aria-pressed="false">赞</button></article>`,
		"wrong text":    `<article data-e2e="comment-item" data-comment-id="456"><a data-sec-uid="author_1">作者</a><p data-e2e="comment-content">修改后</p><button data-e2e="comment-like-button" aria-pressed="false">赞</button></article>`,
		"wrong author":  `<article data-e2e="comment-item" data-comment-id="456"><a data-sec-uid="author_2">作者</a><p data-e2e="comment-content">评论正文</p><button data-e2e="comment-like-button" aria-pressed="false">赞</button></article>`,
	}
	for name, html := range cases {
		t.Run(name, func(t *testing.T) {
			var clicks atomic.Int32
			s := commentLikeFixture(t, html, &clicks)
			defer s.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			req := commentLikeRequest()
			req.BeforeSubmit = func(context.Context) error { t.Fatal("unexpected submit intent"); return nil }
			got, err := runDouyinCommentLike(ctx, t.TempDir()+"\\profile", s.URL+"/video/123", req, func() error { return nil }, true)
			if !errors.Is(err, ErrDouyinInteractionUnknown) || got.State == DouyinInteractionApplied || got.Submitted || clicks.Load() != 0 {
				t.Fatalf("unsafe result=%+v %v clicks=%d", got, err, clicks.Load())
			}
		})
	}
}

func TestLocalDouyinCommentLikeAuthorizationAfterIntent(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	var clicks atomic.Int32
	s := commentLikeFixture(t, commentLikeCard, &clicks)
	defer s.Close()
	var revoked atomic.Bool
	req := commentLikeRequest()
	req.BeforeSubmit = func(context.Context) error { revoked.Store(true); return nil }
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	got, err := runDouyinCommentLike(ctx, t.TempDir()+"\\profile", s.URL+"/video/123", req, func() error {
		if revoked.Load() {
			return context.Canceled
		}
		return nil
	}, true)
	if !errors.Is(err, context.Canceled) || got.State != DouyinInteractionCancelled || !got.Submitted || clicks.Load() != 0 {
		t.Fatalf("authorization=%+v %v clicks=%d", got, err, clicks.Load())
	}
}

func TestLocalDouyinCommentLikeSubmittedNeverClicks(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	var clicks atomic.Int32
	s := commentLikeFixture(t, commentLikeCard, &clicks)
	defer s.Close()
	req := commentLikeRequest()
	req.Submitted, req.BeforeSubmit = true, nil
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	got, err := runDouyinCommentLike(ctx, t.TempDir()+"\\profile", s.URL+"/video/123", req, func() error { return nil }, true)
	if !errors.Is(err, ErrDouyinInteractionUnknown) || got.Code != "receipt_not_found" || !got.Submitted || clicks.Load() != 0 {
		t.Fatalf("submitted=%+v %v clicks=%d", got, err, clicks.Load())
	}
}

func TestLocalDouyinCommentLikeReceiptMismatchAndRejection(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	for _, tc := range []struct {
		name, html, state, code string
	}{
		{"wrong CID", strings.Replace(commentLikeCard, "cid=456&amp;", "cid=457&amp;", 1), DouyinInteractionUnknown, "receipt_mismatch"},
		{"platform rejected", strings.Replace(commentLikeCard, "digg_type=1'", "digg_type=1&amp;reject=1'", 1), DouyinInteractionFailed, "platform_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var clicks atomic.Int32
			s := commentLikeFixture(t, tc.html, &clicks)
			defer s.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			got, err := runDouyinCommentLike(ctx, t.TempDir()+"\\profile", s.URL+"/video/123", commentLikeRequest(), func() error { return nil }, true)
			if err == nil || got.State != tc.state || got.Code != tc.code || !got.Submitted || clicks.Load() != 1 {
				t.Fatalf("receipt=%+v %v clicks=%d", got, err, clicks.Load())
			}
		})
	}
}

func TestLocalDouyinCommentLikeNativeLikedIsReadOnly(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	html := `<div class="dIlRe7ZV xvIrNQth"><div id="tooltip_456"></div><div class="comment-item-info-wrap"><a href="/user/author_1">作者</a></div><p class="FduGc_lz">评论正文</p><div class="comment-item-stats-container"><div class="a2ls69qS"><p class="VpA2NKl1 tOgCrAK_"><svg><path fill="#FE2C55" opacity=".9"></path></svg><span>1</span></p></div></div></div>`
	var clicks atomic.Int32
	s := commentLikeFixture(t, html, &clicks)
	defer s.Close()
	req := commentLikeRequest()
	req.BeforeSubmit = func(context.Context) error { t.Fatal("native liked reached submit"); return nil }
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	got, err := runDouyinCommentLike(ctx, t.TempDir()+"\\profile", s.URL+"/video/123", req, func() error { return nil }, true)
	if err != nil || got.State != DouyinInteractionApplied || got.Code != "already_applied" || got.Submitted || clicks.Load() != 0 {
		t.Fatalf("native liked=%+v %v clicks=%d", got, err, clicks.Load())
	}
}

func TestLocalDouyinCommentLikeNativeUnlikedSingleClick(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	html := `<div data-e2e="comment-item" class="Eh0a5CD4"><div class="dIlRe7ZV xvIrNQth"><div id="tooltip_456"></div><div class="comment-item-info-wrap"><a href="/user/author_1">作者</a></div><p class="FduGc_lz">评论正文</p><div class="comment-item-stats-container"><div class="a2ls69qS"><p class="VpA2NKl1" onclick="this.classList.add('tOgCrAK_');this.querySelector('path').setAttribute('fill','#FE2C55');this.querySelector('span').textContent='1';fetch('/aweme/v1/web/comment/digg/?cid=456&amp;aweme_id=123&amp;digg_type=1',{method:'POST'})"><svg><path fill="#fff" opacity=".9"></path></svg><span>0</span></p></div></div></div></div>`
	var clicks atomic.Int32
	s := commentLikeFixture(t, html, &clicks)
	defer s.Close()
	before := 0
	req := commentLikeRequest()
	req.BeforeSubmit = func(context.Context) error { before++; return nil }
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	got, err := runDouyinCommentLike(ctx, t.TempDir()+"\\profile", s.URL+"/video/123", req, func() error { return nil }, true)
	if err != nil || got.State != DouyinInteractionApplied || got.Code != "applied" || !got.Submitted || before != 1 || clicks.Load() != 1 {
		t.Fatalf("native click=%+v %v before=%d clicks=%d", got, err, before, clicks.Load())
	}
}
