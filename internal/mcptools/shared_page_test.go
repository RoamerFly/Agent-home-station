package mcptools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSharedHTMLExtractsActualTextAndExplicitVideoWithoutExecutingScripts(t *testing.T) {
	body := []byte(`<title>真实正文</title><meta property='og:video' content='/video.mp4?x=1&amp;y=2'><script>stealSecret();</script><article>` + strings.Repeat("真实文章内容", 30) + `</article>`)
	title, text, urls := parseSharedHTML(body, "https://public.example/article")
	if title != "真实正文" || strings.Contains(text, "stealSecret") || len(urls) != 1 || urls[0] != "https://public.example/video.mp4?x=1&y=2" {
		t.Fatal(title, text, urls)
	}
	if _, err := ReadPublicShare(context.Background(), "file:///private", 1000, false); err == nil {
		t.Fatal("local share allowed")
	}
	if cleanShareURL("https://public.example/article?token=private#fragment") != "https://public.example/article" {
		t.Fatal("private params retained")
	}
}

func TestShortVideoRedirectNeverSummarizesAppLandingTextAsVideo(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/short" {
			http.Redirect(w, r, "http://www.kuaishou.com/short-video/3xsynthetic12345", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<article>" + strings.Repeat("这是应用下载推广，不是视频内容。", 50) + "</article>"))
	}))
	defer srv.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, publicSessionKey{}, &publicReadSession{Transport: transport})
	got, err := ReadPublicShare(ctx, "http://v.kuaishou.com/short", 1<<20, false)
	if err == nil || got.Kind == "page" || requests < 2 {
		t.Fatal("app landing page accepted as shared video", got.Kind, err, requests)
	}
}
