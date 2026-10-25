package linuxnative

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEventCursorValidationAndMessageIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, page string
		ok         bool
	}{
		{"ordered", `{"items":[{"cursor":"4","kind":"message.committed","data":{"messageId":"msg_one","threadId":"filehelper","direction":"outgoing"}}],"nextCursor":"4"}`, true},
		{"replayed", `{"items":[{"cursor":"3","kind":"message.committed","data":{}}],"nextCursor":"3"}`, false},
		{"wrong tail", `{"items":[{"cursor":"4","kind":"message.committed","data":{}}],"nextCursor":"5"}`, false},
		{"empty", `{"items":[],"nextCursor":"3"}`, true},
		{"empty wrong tail", `{"items":[],"nextCursor":"9"}`, false},
		{"numeric cursor", `{"items":[{"cursor":4,"kind":"message.committed","data":{}}],"nextCursor":"4"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/accounts/test/events" || r.URL.Query().Get("after") != "3" || r.URL.Query().Get("limit") != "100" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("event request contract mismatch")
				}
				write(w, tc.page)
			}))
			defer server.Close()
			_, err := testSender(t, server.URL).Events(context.Background(), "3")
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for _, account := range []string{"test", "other"} {
		t.Run(account, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				write(w, `{"id":"msg_one","accountId":"`+account+`","threadId":"filehelper","type":1,"direction":"incoming","body":"text"}`)
			}))
			defer server.Close()
			_, err := testSender(t, server.URL).Message(context.Background(), "msg_one")
			if (err == nil) != (account == "test") {
				t.Fatalf("cross-account result: %v", err)
			}
		})
	}
}
