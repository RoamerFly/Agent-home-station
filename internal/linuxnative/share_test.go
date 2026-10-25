package linuxnative

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVideoPlaybackShareUsesNativeCardContract(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["kind"] != "share" || in["title"] != "动画片段" || in["url"] != "https://www.bilibili.com/video/BV1qP4y187Lf/" || in["sourceId"] != nil {
			t.Error("wrong native share payload", in)
		}
		w.WriteHeader(202)
		write(w, `{"id":"card-one","accountId":"test","threadId":"filehelper","state":"accepted","serverMessageId":"123"}`)
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	status, e := s.Submit(context.Background(), ToolSend{ThreadID: "filehelper", Kind: "share", Title: "动画片段", URL: "https://www.bilibili.com/video/BV1qP4y187Lf/", IdempotencyKey: "same-card"})
	if e != nil || status.State != "accepted" || calls != 1 {
		t.Fatal(status, e, calls)
	}
	if _, e = s.Submit(context.Background(), ToolSend{ThreadID: "filehelper", Kind: "share", Title: "动画片段", URL: "file:///private", IdempotencyKey: "bad-card"}); e == nil || calls != 1 {
		t.Fatal("invalid card reached native")
	}
}
