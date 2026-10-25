package linuxnative

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"wechat-bot/internal/sending"
)

func TestMediaPreparationFailureRemainsKnownFailure(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatal("terminal preparation failure must not poll")
			return
		}
		posts++
		write(w, `{"id":"video-one","state":"failed","phase":"preparing","threadId":"filehelper","reason":"media_processing_failed"}`)
	}))
	defer server.Close()
	status, err := testSender(t, server.URL).SendMedia(context.Background(), "filehelper", "video", "owned-source")
	if posts != 1 || status.State != "failed" || !errors.Is(err, sending.ErrRejected) || errors.Is(err, sending.ErrOutcomeUnknown) || sending.FailureReason(err) != "media_processing_failed" || sending.IntentID(err) != "video-one" {
		t.Fatal("known failure lost", status, err)
	}
}
