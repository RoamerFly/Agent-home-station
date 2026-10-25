package linuxnative

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMemberCatalogRetriesTransientReadWithoutWriting(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		if calls.Add(1) == 1 {
			h := w.(http.Hijacker)
			c, _, _ := h.Hijack()
			c.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[{"id":"person-one","username":"person-one","displayName":"昵称","roomDisplayName":"群名片"}],"nextCursor":null}`))
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	page, err := s.Catalog(context.Background(), "members", "room@chatroom", "", "")
	if err != nil || len(page.Items) != 1 || page.Items[0]["roomDisplayName"] != "群名片" || calls.Load() != 2 {
		t.Fatal("read recovery failed", err, page, calls.Load())
	}
}

func TestCatalogErrorsAreTypedAndNeverExposeRawBodies(t *testing.T) {
	for _, tc := range []struct {
		status       int
		cursor, code string
	}{{401, "", "catalog_auth_denied"}, {404, "", "catalog_not_found"}, {400, "expired-cursor", "catalog_cursor_invalid"}, {503, "", "catalog_upstream_unavailable"}} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"error":"SECRET-UPSTREAM"}`))
			}))
			defer server.Close()
			s := testSender(t, server.URL)
			_, err := s.Catalog(context.Background(), "members", "room@chatroom", "", tc.cursor)
			var typed *CatalogError
			if !errors.As(err, &typed) || typed.Code != tc.code || typed.HTTPStatus != tc.status {
				t.Fatal("wrong cause", err)
			}
		})
	}
	s := testSender(t, "http://127.0.0.1:6174")
	if _, err := s.Catalog(context.Background(), "members", "private-friend", "", ""); err == nil {
		t.Fatal("private treated as group")
	}
}
