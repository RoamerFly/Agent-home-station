package linuxnative

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuotedMessagePreservesTypedTextAndScopesOriginal(t *testing.T) {
	account := "test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"accountId":"`+account+`","messageId":"parent","threadId":"room@chatroom","state":"resolved","message":{"id":"original","accountId":"test","threadId":"room@chatroom","type":1,"direction":"outgoing","serverId":"777","body":"音乐原文件"}}`)
	}))
	defer srv.Close()
	s := testSender(t, srv.URL)
	quote, err := s.QuotedMessage(context.Background(), "parent")
	if err != nil || quote.Message == nil || quote.Message.Body != "音乐原文件" {
		t.Fatal(quote, err)
	}
	account = "other-account"
	if _, err = s.QuotedMessage(context.Background(), "parent"); err == nil {
		t.Fatal("cross-account quote accepted")
	}
}
