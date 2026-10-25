package linuxnative

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCatalogMetadataOperationScopesAndFixedRoutes(t *testing.T) {
	account, kind := "test", "metadata"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing token")
		}
		switch r.URL.Path {
		case "/v1/accounts/test/group-sync", "/v1/accounts/test/contact-sync", "/v1/accounts/test/groups/new@chatroom/refresh":
			if r.Method != "POST" {
				t.Error(r.Method)
			}
			w.WriteHeader(202)
		case "/v1/accounts/test/operations/meta-1":
		default:
			t.Error("unexpected request", r.URL.Path)
		}
		w.Write([]byte(`{"id":"meta-1","accountId":"` + account + `","kind":"` + kind + `","state":"succeeded","result":{"recordCount":2,"complete":true}}`))
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	ctx := context.Background()
	if _, err := s.StartCatalogSync(ctx, "groups"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartCatalogSync(ctx, "contacts"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshGroupMetadata(ctx, "new@chatroom"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshGroupMetadata(ctx, "friend"); err == nil {
		t.Fatal("friend admitted as group")
	}
	if _, err := s.StartCatalogSync(ctx, "../auth/login"); err == nil {
		t.Fatal("arbitrary route allowed")
	}
	account = "other"
	if _, err := s.CatalogSyncStatus(ctx, "meta-1"); err == nil {
		t.Fatal("cross account operation accepted")
	}
	account = "test"
	kind = "login"
	if _, err := s.CatalogSyncStatus(ctx, "meta-1"); err == nil {
		t.Fatal("login exposed as catalog progress")
	}
}

func TestGroupsFollowsCursorWithoutTruncatingDirectory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "next" {
			w.Write([]byte(`{"items":[{"id":"second@chatroom","displayName":"Second"}],"nextCursor":null}`))
			return
		}
		w.Write([]byte(`{"items":[{"id":"first@chatroom","displayName":"First"}],"nextCursor":"next"}`))
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	items, err := s.Groups(context.Background(), "")
	if err != nil || len(items) != 2 || items[1].Name != "Second" {
		t.Fatal("directory truncated", err, items)
	}
}
