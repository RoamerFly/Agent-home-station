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

func TestChannelsShareURLRejectsWrongMessageRoomAccountOrObject(t *testing.T) {
	for _, tc := range []struct {
		account, room, message, object string
		ok                             bool
	}{
		{"test", "room@chatroom", "msg_one", "18446744073709551614", true},
		{"other", "room@chatroom", "msg_one", "18446744073709551614", false},
		{"test", "other@chatroom", "msg_one", "18446744073709551614", false},
		{"test", "room@chatroom", "msg_other", "18446744073709551614", false},
		{"test", "room@chatroom", "msg_one", "18446744073709551613", false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/v1/accounts/test/messages/msg_one/channels-share-url" || r.Header.Get("Authorization") != "Bearer test-token" || r.ContentLength > 0 {
				t.Error("lookup must submit only a server-owned card reference")
			}
			write(w, `{"accountId":"`+tc.account+`","threadId":"`+tc.room+`","messageId":"`+tc.message+`","objectId":"`+tc.object+`","shareUrl":"https://weixin.qq.com/sph/Synthetic12"}`)
		}))
		link, err := testSender(t, server.URL).ChannelsShareURL(context.Background(), "msg_one", "room@chatroom", "18446744073709551614")
		server.Close()
		if (err == nil) != tc.ok || (!tc.ok && link != "") {
			t.Fatal("lookup identity escaped its scope", tc, err)
		}
	}
}

func TestChannelsShareURLPreservesOnlyAllowlistedNativeFailures(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"channels_share_identity_missing", "channels_share_identity_missing"},
		{"channels_share_identity_mismatch", "channels_share_identity_mismatch"},
		{"private-upstream-body", "channels_native_share_unavailable"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			write(w, `{"error":{"code":"`+tc.code+`","message":"private details"}}`)
		}))
		_, err := testSender(t, server.URL).ChannelsShareURL(context.Background(), "msg_one", "room@chatroom", "123")
		server.Close()
		if err == nil || err.Error() != tc.want {
			t.Fatal("unsafe or unhelpful error", err)
		}
	}
}
