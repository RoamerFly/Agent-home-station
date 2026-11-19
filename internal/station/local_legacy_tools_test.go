package station

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
)

func TestLegacyToolSessionReuseAndExpiry(t *testing.T) {
	logins, calls := 0, 0
	expired, fail := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" {
			logins++
			http.SetCookie(w, &http.Cookie{Name: "session", Value: fmt.Sprint(logins), Path: "/"})
			fmt.Fprint(w, `{"authenticated":true,"csrf":"csrf"}`)
			return
		}
		cookie, err := r.Cookie("session")
		if err != nil || (expired && cookie.Value == "1") {
			w.WriteHeader(401)
			return
		}
		calls++
		if fail {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	c := &legacyToolClient{base: server.URL, client: &http.Client{Jar: jar}, password: func() ([]byte, error) { return []byte("synthetic"), nil }}
	if err := c.login(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if _, err := c.request(context.Background(), "GET", "/api/plugin-packages", nil); err != nil {
			t.Fatal(err)
		}
	}
	if logins != 1 || calls != 25 {
		t.Fatalf("logins=%d calls=%d", logins, calls)
	}
	expired = true
	if _, err := c.request(context.Background(), "POST", "/start", nil); err != nil {
		t.Fatal(err)
	}
	if logins != 2 || calls != 26 {
		t.Fatalf("expiry logins=%d calls=%d", logins, calls)
	}
	fail = true
	if _, err := c.request(context.Background(), "POST", "/stop", nil); err == nil {
		t.Fatal("expected failure")
	}
	if logins != 2 || calls != 27 {
		t.Fatal("uncertain mutation was replayed")
	}
}
func TestUnboundToolIsNotAdvertisedReady(t *testing.T) {
	s := &localProjectTools{home: &localHomeService{root: t.TempDir()}}
	v, err := s.projectView(t.Context(), nil, "visual-generation", "视觉生成")
	if err != nil || v.RuntimeReady || v.State != "not_available" || v.Reason == "" {
		t.Fatalf("view=%+v err=%v", v, err)
	}
}

func TestPublicToolsAreNotApplicationToolCandidates(t *testing.T) {
	s := &localProjectTools{}
	ids := s.publicToolIDs()
	for _, id := range []string{
		"document-workshop", "media-understanding", "share-reader", "web-search",
		"music", "project-development", "rvc", "yue", "station-speech",
	} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("public tool %q was not classified as public: %#v", id, ids)
		}
	}
	if _, ok := ids["wechat-private-example"]; ok {
		t.Fatal("application-only package was classified as public")
	}
}
