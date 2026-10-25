package linuxnative

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpeakerNamePriorityExactIdentityAndRoomScopedCache(t *testing.T) {
	contacts, members := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("name lookup mutated directory")
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/v1/accounts/test/contacts/remarked":
			contacts++
			fmt.Fprint(w, `{"id":"remarked","username":"remarked","remark":"我的备注","nickname":"昵称"}`)
		case "/v1/accounts/test/contacts/peer":
			contacts++
			fmt.Fprint(w, `{"id":"peer","username":"peer","remark":"","nickname":"普通昵称"}`)
		case "/v1/accounts/test/contacts/wrong":
			fmt.Fprint(w, `{"id":"someone-else","username":"someone-else","remark":"不得套用"}`)
		case "/v1/accounts/test/groups/one@chatroom/members/peer":
			members++
			fmt.Fprint(w, `{"groupId":"one@chatroom","username":"peer","state":"present","member":{"id":"peer","username":"peer","displayName":"昵称","roomDisplayName":"一群昵称"}}`)
		case "/v1/accounts/test/groups/two@chatroom/members/peer":
			members++
			fmt.Fprint(w, `{"groupId":"two@chatroom","username":"peer","state":"present","member":{"id":"peer","username":"peer","displayName":"昵称","roomDisplayName":"二群昵称"}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	for _, tc := range []struct{ thread, id, name, source string }{{"one@chatroom", "remarked", "我的备注", "remark"}, {"one@chatroom", "peer", "一群昵称", "room"}, {"two@chatroom", "peer", "二群昵称", "room"}, {"friend", "peer", "普通昵称", "nickname"}, {"one@chatroom", "missing", "missing", "id"}} {
		name, err := s.ResolveSpeaker(context.Background(), tc.thread, tc.id)
		if err != nil || name.Text != tc.name || name.Source != tc.source {
			t.Fatal(tc, name, err)
		}
	}
	before := contacts
	name, err := s.ResolveSpeaker(context.Background(), "one@chatroom", "peer")
	if err != nil || name.Text != "一群昵称" || contacts != before || members != 2 {
		t.Fatal("cache crossed room or re-read", name, contacts, members)
	}
	if _, err = s.ResolveSpeaker(context.Background(), "one@chatroom", "wrong"); err == nil {
		t.Fatal("different contact identity accepted")
	}
}

func TestGroupMemberWrongScopeIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts/test/contacts/peer" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"groupId":"other@chatroom","username":"peer","member":{"id":"peer","username":"peer","roomDisplayName":"其他群昵称"}}`)
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	if _, err := s.ResolveSpeaker(context.Background(), "one@chatroom", "peer"); err == nil {
		t.Fatal("another room's nickname used")
	}
}
