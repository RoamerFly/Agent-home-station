package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestFetchMissingURLReturnsActionableSafeDiagnostic(t *testing.T) {
	h, _ := New(nil)
	for _, args := range []string{`{}`, `{"query":"deepseek"}`, `null`, `{"url":" "}`} {
		_, err := h.Call(context.Background(), "web_fetch", json.RawMessage(args))
		var web *WebError
		if !errors.As(err, &web) || web.Code != "url_required" || !strings.Contains(FailureDescription(err), "url") {
			t.Fatal("missing diagnostic", args, err)
		}
	}
	for _, err := range []error{errors.New("https://private.example/?token=secret"), &net.DNSError{Err: "secret", Name: "private.example"}} {
		if s := FailureDescription(err); strings.Contains(s, "secret") || strings.Contains(s, "private.example") {
			t.Fatal("credential/url leak", s)
		}
	}
}

func TestMediaCandidatesResolveAndBoundWithoutFetching(t *testing.T) {
	body := `<audio src="../horse.mp3"></audio><source src='/movie.mp4?x=1&amp;y=2'><a href="/movie.mp4?x=1&amp;y=2">same</a><a href="javascript:alert(1).mp3">bad</a><img src="data:image/png;base64,xyz"><a href="https://user:pass@example.com/a.mp4">credential</a>`
	links := ExtractMediaLinks(body, "https://example.com/html/page")
	if len(links) != 2 || links[0] != "https://example.com/horse.mp3" || links[1] != "https://example.com/movie.mp4?x=1&y=2" {
		t.Fatal(links)
	}
}

func TestMediaTypeRejectsPlayerOrLoginHTML(t *testing.T) {
	for _, kind := range []string{"voice", "video", "image", "file"} {
		if _, err := ValidateMediaType(kind, "video/mp4", []byte("<!DOCTYPE html><html>Login</html>")); err == nil {
			t.Fatal("HTML disguised as media", kind)
		}
	}
	if _, err := ValidateMediaType("video", "audio/mpeg", []byte{0xff, 0xfb, 1, 2}); err == nil {
		t.Fatal("audio accepted as video")
	}
	if typ, err := ValidateMediaType("voice", "audio/mpeg", []byte{0xff, 0xfb, 1, 2}); err != nil || typ != "audio/mpeg" {
		t.Fatal(typ, err)
	}
}
