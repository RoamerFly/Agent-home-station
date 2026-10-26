package mcptools

import (
	"context"
	"strings"
	"testing"
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
