package admin

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestReferencedScriptsHaveExecutableContentType(t *testing.T) {
	f := setup(t)
	code, html, _ := f.request("GET", "/", nil, false, false)
	if code != 200 {
		t.Fatal(code)
	}
	refs := regexp.MustCompile(`<script[^>]+src="([^"]+)"`).FindAllSubmatch(html, -1)
	if len(refs) == 0 {
		t.Fatal("no scripts")
	}
	for _, ref := range refs {
		r, err := http.Get(f.server.URL + string(ref[1]))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/javascript") {
			t.Fatalf("browser cannot execute %s: status=%d type=%s", ref[1], r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
}
