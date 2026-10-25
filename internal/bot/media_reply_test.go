package bot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestMediaCaptionWaitsForAllReceiptsAndSurvivesRestart(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	c.Config.ShowAILabel = false
	if !w.deferMediaReply(in, []string{"找到啦，这个是那一段～"}, 1) {
		t.Fatal("caption not durable")
	}
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "source", Kind: "video", Title: "clip.mp4"})
	w.reconcileMediaReplies(context.Background())
	if n.calls != 0 {
		t.Fatal("caption preceded attachment receipt")
	}
	if e := w.loadRuntime("server-1"); e != nil {
		t.Fatal(e)
	}
	d := w.runtime.Media[0]
	d.State = "accepted"
	w.reconcileMediaReplies(context.Background())
	if n.calls != 0 {
		t.Fatal("caption ignored natural wait")
	}
	r := w.runtime.MediaReplies[0]
	r.Next = time.Now().Add(-time.Second)
	w.reconcileMediaReplies(context.Background())
	if n.calls != 1 || r.State != "accepted" {
		t.Fatal("caption not sent once after receipt", n.calls, r.State)
	}
	w.reconcileMediaReplies(context.Background())
	if n.calls != 1 {
		t.Fatal("caption repeated")
	}
}
func TestCancelledOrUncertainMediaNeverSendsSuccessCaption(t *testing.T) {
	for _, state := range []string{"stopped", "needs_review"} {
		w, n, c, in := mediaWorker(t)
		w.deferMediaReply(in, []string{"给你，找到啦"}, 1)
		w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "source", Kind: "voice"})
		w.runtime.Media[0].State = state
		w.reconcileMediaReplies(context.Background())
		if n.calls != 0 || w.runtime.MediaReplies[0].State != "stopped" {
			t.Fatal("false success caption", state)
		}
	}
}

func TestDeliveredMusicCaptionNoLongerPromisesFutureSend(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	in.QuoteSharedMessage = true
	in.QuoteServerID = "77"
	w.deferMediaReply(in, []string{"找到一首轻快的 Cloud Dancer，分几段发你～"}, 1)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "old", Kind: "voice"})
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	if w.runtime.MediaReplies[0].Quote == nil || w.runtime.MediaReplies[0].Quote.ServerID != "77" {
		t.Fatal("quote reference lost on restart")
	}
	w.runtime.Media[0].State = "accepted"
	w.reconcileMediaReplies(context.Background())
	w.runtime.MediaReplies[0].Next = time.Now().Add(-time.Second)
	w.reconcileMediaReplies(context.Background())
	if n.calls != 1 {
		t.Fatal("accepted caption not sent")
	}
	scope := c.Store.Context(in, c.Config, time.Now())
	found := false
	for _, turn := range scope.Messages {
		if turn.Role == "assistant" && turn.Media == nil {
			found = true
			if strings.Contains(turn.Text, "分几段发你") {
				t.Fatal("completed delivery still promised future send")
			}
		}
	}
	if !found {
		t.Fatal("delivered caption not remembered")
	}
}

func TestNewInboxInstructionIsReadBeforeOldReadyCaption(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.deferMediaReply(in, []string{"旧歌曲的迟到介绍"}, 1)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "old", Kind: "voice"})
	w.runtime.Media[0].State = "accepted"
	w.runtime.MediaReplies[0].Next = time.Now().Add(-time.Second)
	if err := w.saveRuntime(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pages := 0
	w.source = &fakeSource{events: []linuxnative.Event{fixtureEvent("1", "new-song", "filehelper", "incoming", "live")}, messages: map[string]linuxnative.Message{"new-song": fixtureMessage("new-song", "filehelper", "重新找一首，traveling light", "incoming")}, onPage: func(string) {
		pages++
		if pages > 1 {
			cancel()
		}
	}}
	w.config.PollInterval = time.Millisecond
	if err := w.runDurable(ctx, filepath.Join(w.config.StateDir, "checkpoint.json"), checkpoint{ServerID: "server-1", Cursor: "0"}); err != nil {
		t.Fatal(err)
	}
	if n.calls != 0 || w.runtime.MediaReplies[0].State != "stopped" {
		t.Fatal("old caption emitted before reading the new request", n.calls, w.runtime.MediaReplies[0].State)
	}
}
