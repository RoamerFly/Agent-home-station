package linuxnative

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountsUseIndependentIdentityCaches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("identity lookup wrote native state")
			http.Error(w, "denied", 405)
			return
		}
		id := "test"
		if strings.Contains(r.URL.Path, "/other/") {
			id = "other"
		}
		var body any
		switch {
		case r.URL.Path == "/v1/accounts":
			body = map[string]any{"items": []map[string]any{{"id": "test", "alias": "primary", "connection": "online", "token": "not-exported"}}}
		case strings.HasSuffix(r.URL.Path, "/threads/filehelper/messages"):
			body = map[string]any{"items": []map[string]any{{"id": "out", "threadId": "filehelper", "direction": "outgoing", "senderId": "same-self", "type": 1}}}
		case strings.HasSuffix(r.URL.Path, "/contacts/same-self"):
			body = map[string]any{"id": "same-self", "nickname": id + "的昵称"}
		default:
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	first := testSender(t, server.URL)
	second, err := first.ForAccount("other")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Sender{first, second, first} {
		name, err := s.DisplayName(context.Background())
		if err != nil || name != s.Account()+"的昵称" {
			t.Fatal("nickname cache crossed accounts", name, err)
		}
	}
	list, err := first.Accounts(context.Background())
	b, _ := json.Marshal(list)
	if err != nil || strings.Contains(string(b), "token") {
		t.Fatal("account metadata leaked fields", err)
	}
	for _, id := range []string{"../other", "bad/path", ""} {
		if _, err := first.ForAccount(id); err == nil {
			t.Fatal("unsafe account accepted", id)
		}
	}
}
