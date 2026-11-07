//go:build windows

package stationbrowser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/hostfile"
)

func TestDouyinHomeLoginChallengeAndLoginTopic(t *testing.T) {
	if looksLikeLoginOrChallenge(renderedDouyinPage{Title: "普通文章", Text: "介绍登录流程"}) {
		t.Fatal("ordinary login-topic text was rejected")
	}
	if looksLikeLoginOrChallenge(renderedDouyinPage{Title: "普通文章", LoginForm: false, Challenge: false}) {
		t.Fatal("hidden login form state was rejected")
	}
	if !looksLikeLoginOrChallenge(renderedDouyinPage{Title: "登录", LoginForm: true}) {
		t.Fatal("login form was accepted")
	}
	if !looksLikeLoginOrChallenge(renderedDouyinPage{Title: "普通", Challenge: true}) {
		t.Fatal("challenge was accepted")
	}
}

func TestLocalDouyinHomeWaitsForDelayedVisibleBody(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge is not installed in Program Files")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<title>fixture</title><div id=x></div><script>setTimeout(()=>x.textContent='delayed visible body',350)</script>`))
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got, err := readDouyinHome(ctx, t.TempDir()+"\\profile", s.URL, func() error { return nil }, true)
	if err != nil || got.Text != "delayed visible body" {
		t.Fatalf("delayed text: %#v %v", got, err)
	}
}

func TestLocalDouyinHomeRejectsVisibleLoginForm(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge is not installed in Program Files")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<title>登录</title><form action=/login><input type=password>请登录</form>`))
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := readDouyinHome(ctx, t.TempDir()+"\\profile", s.URL, func() error { return nil }, true)
	if !errors.Is(err, ErrDouyinWebLoginRequired) {
		t.Fatalf("visible login = %v", err)
	}
}

func TestDouyinHomeFixedTargetAndCancellation(t *testing.T) {
	if _, err := readDouyinHome(context.Background(), t.TempDir(), "https://example.invalid/", func() error { return nil }, false); !errors.Is(err, ErrDouyinHomeUnavailable) {
		t.Fatalf("cross origin = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readDouyinHome(ctx, t.TempDir(), "http://fixture.invalid/", func() error { return nil }, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestDouyinHomeTextSizeBoundary(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<title>fixture</title><p>" + strings.Repeat("中", maxDouyinHomeTextRunes+1) + "</p>"))
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	page, err := readDouyinHome(ctx, t.TempDir()+"\\profile", s.URL, func() error { return nil }, true)
	if !errors.Is(err, ErrDouyinWebTextSize) || page.Text != "" {
		t.Fatalf("oversized result was not rejected: %v text length=%d", err, len(page.Text))
	}
}

func TestLocalDouyinHomeHiddenLoginFormIsNotAChallenge(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<title>fixture</title><form action=/login style="display:none"><input type=password></form><p>介绍登录流程</p>`))
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	page, err := readDouyinHome(ctx, t.TempDir()+"\\profile", s.URL, func() error { return nil }, true)
	if err != nil || strings.TrimSpace(page.Text) != "介绍登录流程" {
		t.Fatalf("hidden form blocked ordinary text: %#v %v", page, err)
	}
}

func TestLocalDouyinHomeRedirectDoesNotReturnAnotherSource(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge unavailable")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<title>other</title>another source"))
	}))
	defer other.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	page, err := readDouyinHome(ctx, t.TempDir()+"\\profile", s.URL, func() error { return nil }, true)
	if !errors.Is(err, ErrDouyinHomeUnavailable) || page.Text != "" {
		t.Fatalf("cross-source redirect returned data: %#v %v", page, err)
	}
}

func TestLocalDouyinHomeReadsOnlyVisibleProfileText(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge is not installed in Program Files")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			_, _ = w.Write([]byte(`<title>fixture</title><script>document.cookie='home=ok;Max-Age=3600;path=/'</script>set`))
			return
		}
		if r.Header.Get("Cookie") != "" {
			_, _ = w.Write([]byte(`<title>fixture</title>private visible body`))
			return
		}
		_, _ = w.Write([]byte(`<title>fixture</title>public body`))
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	p := t.TempDir() + "\\profile"
	if _, err := readDouyinHome(ctx, p, s.URL+"/set", func() error { return nil }, true); err != nil {
		t.Fatal(err)
	}
	got, err := readDouyinHome(ctx, p, s.URL+"/read", func() error { return nil }, true)
	if err != nil || got.Text != "private visible body" {
		t.Fatalf("same profile: %#v %v", got, err)
	}
	other, err := readDouyinHome(ctx, t.TempDir()+"\\other", s.URL+"/read", func() error { return nil }, true)
	if err != nil || other.Text != "public body" {
		t.Fatalf("other profile: %#v %v", other, err)
	}
}

func TestLocalDouyinHomeExclusiveProfile(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge is not installed in Program Files")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<title>x</title>stable body`)) }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := t.TempDir() + "\\profile"
	lock, err := DouyinLoginProfileLockPath(p)
	if err != nil {
		t.Fatal(err)
	}
	release, err := hostfile.TryLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := readDouyinHome(ctx, p, s.URL, func() error { return nil }, true); !errors.Is(err, ErrDouyinLoginActive) {
		t.Fatalf("profile lock = %v", err)
	}
}

func TestLocalDouyinHomeAuthorizationRechecked(t *testing.T) {
	if !Available() {
		t.Skip("Microsoft Edge is not installed in Program Files")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<title>fixture</title>body`)) }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	calls := 0
	_, err := readDouyinHome(ctx, t.TempDir()+"\\profile", s.URL, func() error {
		calls++
		if calls > 1 {
			return context.Canceled
		}
		return nil
	}, true)
	if err != context.Canceled {
		t.Fatalf("authorization revocation = %v", err)
	}
}
