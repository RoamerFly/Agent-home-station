package linuxnative

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChannelsSourceRequiresExactMessageAndAccount(t *testing.T) {
	for _, account := range []string{"test", "another"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/accounts/test/messages/msg_one/channels-media" || r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("wrong source request")
			}
			write(w, `{"state":"available","messageId":"msg_one","accountId":"`+account+`","threadId":"room@chatroom","mediaUrl":"https://finder.video.qq.com/video?token=synthetic","decodeKey":"18446744073709551614"}`)
		}))
		v, err := testSender(t, s.URL).ChannelsMedia(context.Background(), "msg_one")
		s.Close()
		if account == "test" {
			if err != nil || v.DecodeKey != "18446744073709551614" {
				t.Fatal(err, v)
			}
		} else if err == nil || strings.Contains(err.Error(), "synthetic") {
			t.Fatal("wrong account or leaked locator", err)
		}
	}
}
