package bot

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestVoiceAndFileSerializeAndPersistRandomGap(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	c.Config.Permissions.SendFile = true
	w.config.Options.RandomDelay = true
	w.config.Options.DelayMinSeconds = 3
	w.config.Options.DelayMaxSeconds = 8
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "voice-a", Kind: "voice", Title: "song.wav"})
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "file-b", Kind: "file", Title: "song.flac"})
	if n.sends != 1 || w.runtime.Media[1].State != "prepared" {
		t.Fatal("formats sent concurrently", n.sends)
	}
	first, second := w.runtime.Media[0], w.runtime.Media[1]
	n.status.State = "accepted"
	first.Next = time.Time{}
	w.reconcileMedia(context.Background())
	gap := second.NotBefore.Sub(first.CompletedAt)
	if n.sends != 1 || gap < 3*time.Second || gap > 8*time.Second {
		t.Fatal("random gap missing", gap, n.sends)
	}
	deadline := second.NotBefore
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	second = w.runtime.Media[1]
	if !second.NotBefore.Equal(deadline) {
		t.Fatal("restart lost pacing deadline")
	}
	second.NotBefore = time.Now().Add(-time.Second)
	second.Next = time.Time{}
	w.runtime.SendGates = nil
	w.reconcileMedia(context.Background())
	if n.sends != 2 {
		t.Fatal("file not delivered after gap", n.sends)
	}
}

type mediaFake struct {
	onStatus func()
	fakeSender
	status                  linuxnative.ToolStatus
	sends, retries, cancels int
	keys                    map[string]bool
}

func (n *mediaFake) Submit(_ context.Context, r linuxnative.ToolSend) (linuxnative.ToolStatus, error) {
	n.sends++
	n.keys[r.IdempotencyKey] = true
	n.status = linuxnative.ToolStatus{ID: "job-test", State: "running", Phase: "pacing", ThreadID: r.ThreadID, SourceID: r.SourceID}
	return n.status, nil
}
func (n *mediaFake) LookupSend(_ context.Context, key string) (linuxnative.ToolStatus, error) {
	if !n.keys[key] {
		return linuxnative.ToolStatus{}, linuxnative.ErrToolNotFound
	}
	return n.status, nil
}
func (n *mediaFake) ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error) {
	if n.onStatus != nil {
		n.onStatus()
	}
	return n.status, nil
}
func TestUpdateDrainWaitsForMediaWithoutCancellingOrTakingNewInbox(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "source", Kind: "voice", Title: "track.wav"})
	w.runtime.Media[0].Next = time.Time{}
	w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent("1", "next", "filehelper", "incoming", "wechat"), Phase: "received", Updated: time.Now().UTC()})
	if e := w.saveRuntime(); e != nil {
		t.Fatal(e)
	}
	reads := 0
	n.onStatus = func() {
		reads++
		if reads >= 2 {
			n.status.State = "accepted"
			n.status.ServerID = "123"
		}
	}
	w.config.Draining = func() bool { return true }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := w.runDurable(ctx, filepath.Join(w.config.StateDir, "checkpoint.json"), checkpoint{ServerID: "server-1"})
	if e != nil || ctx.Err() != nil || reads < 2 || n.cancels != 0 || w.runtime.Media[0].State != "accepted" {
		t.Fatal("update interrupted media", e, reads, n.cancels)
	}
	if w.runtime.Inbox[0].Phase != "received" || w.source.(*fakeSource).reads != 0 {
		t.Fatal("drain consumed next message")
	}
}
func (n *mediaFake) RetryMediaJob(context.Context, string) (linuxnative.ToolStatus, error) {
	n.retries++
	n.status.State = "running"
	n.status.Retryable = false
	return n.status, nil
}
func (n *mediaFake) CancelMediaJob(context.Context, string) (linuxnative.ToolStatus, error) {
	n.cancels++
	n.status.State = "cancelled"
	n.status.Retryable = true
	return n.status, nil
}
func mediaWorker(t *testing.T) (*Worker, *mediaFake, *companion.Engine, companion.Input) {
	t.Helper()
	store, e := companion.NewStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	c := companion.Defaults()
	c.Enabled = true
	c.Permissions.Remember = true
	engine, e := companion.New(c, store, generatedModel{}, "test")
	if e != nil {
		t.Fatal(e)
	}
	n := &mediaFake{keys: map[string]bool{}}
	opts := DefaultOptions()
	w, e := New(Config{StateDir: t.TempDir(), Options: &opts, AllowedThreads: []string{"filehelper", "room@chatroom"}}, &fakeSource{}, engine, n)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.loadRuntime("server-1"); e != nil {
		t.Fatal(e)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "incoming", Mode: "direct", Text: "发音乐"}
	if e = store.Observe(in, c, time.Now()); e != nil {
		t.Fatal(e)
	}
	return w, n, engine, in
}

func TestModelFileIntentCancelsOnlyRequestersOldVoiceAndSurvivesRestart(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	in.Thread, in.Sender = "room@chatroom", "requester"
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "voice-source", Kind: "voice", Title: "song.wav"})
	other := in
	other.Sender, other.TraceID = "other-user", "other-trace"
	w.queueMedia(context.Background(), c, other, companion.GeneratedMedia{ID: "other-source", Kind: "voice", Title: "other.wav"})
	newRequest := in
	newRequest.TraceID, newRequest.Text = "new-request", "还是原版吧"
	w.cancelVoiceForIntent(context.Background(), newRequest, "file")
	if n.cancels != 1 || !w.runtime.Media[0].UserCancelled || w.runtime.Media[0].State != "cancelling" || w.runtime.Media[1].UserCancelled {
		t.Fatal("file intent failed or interrupted someone else", n.cancels, w.runtime.Media)
	}
	w.runtime.Media[0].Next = time.Time{}
	n.status.SourceID = "voice-source"
	w.reconcileMedia(context.Background())
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	if !w.runtime.Media[0].UserCancelled || w.runtime.Media[0].State != "stopped" {
		t.Fatal("cancellation lost on restart")
	}
}
func TestMediaPacingDoesNotBlockStopAndPersistsCancelledMemory(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	start := time.Now()
	if !w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "alias", SourceID: "cached-source", AssetID: "asset-test", Kind: "voice", Title: "song.wav"}) {
		t.Fatal("not handled")
	}
	if time.Since(start) > time.Second || n.sends != 1 || w.runtime.Media[0].State != "running" {
		t.Fatal("voice blocked or not queued")
	}
	m := fixtureMessage("stop", "filehelper", "发我音乐文件吧，不用发语音了", "outgoing")
	if !w.stopRequestedVoice(context.Background(), m) || n.cancels != 1 {
		t.Fatal("stop ignored")
	}
	d := w.runtime.Media[0]
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if d.State != "stopped" || !d.UserCancelled || n.retries != 0 || n.sends != 1 {
		t.Fatal("cancelled voice replayed")
	}
	scope := c.Store.Context(in, c.Config, time.Now())
	found := false
	for _, turn := range scope.Messages {
		if turn.Media != nil {
			found = true
			if turn.Media.State != "stopped" || turn.Media.SourceID != "cached-source" || turn.Media.AssetID != "asset-test" {
				t.Fatalf("wrong memory: %+v", turn.Media)
			}
		}
	}
	if !found {
		t.Fatal("voice missing from memory")
	}
	// Reload the real durable file: cancellation survives service restart.
	if e := w.loadRuntime("server-1"); e != nil {
		t.Fatal(e)
	}
	w.runtime.Media[0].Next = time.Time{}
	w.reconcileMedia(context.Background())
	if n.retries != 0 {
		t.Fatal("restart retried user stop")
	}
}

func TestNewSongCancelsOldVoiceAndItsLateCaptionAcrossRestart(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.deferMediaReply(in, []string{"找到 Cloud Dancer，分几段发你"}, 1)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "old", Kind: "voice", Title: "Cloud Dancer"})
	message := fixtureMessage("new-song", "filehelper", "重新找一首，traveling light", "outgoing")
	if !w.stopRequestedVoice(context.Background(), message) || n.cancels != 1 || w.runtime.MediaReplies[0].State != "stopped" {
		t.Fatal("new request did not supersede old voice/caption")
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	w.reconcileMediaReplies(context.Background())
	w.runtime.Media[0].Next = time.Time{}
	w.reconcileMedia(context.Background())
	if n.calls != 0 || n.retries != 0 || !w.runtime.Media[0].UserCancelled {
		t.Fatal("stale caption or audio resumed")
	}
	for _, text := range []string{"不要换歌", "不用重新找，继续这首", "你们换首歌吧"} {
		if text != "你们换首歌吧" && replacesVoice(text) {
			t.Fatal("negated instruction cancelled current song")
		}
	}
}

func TestUpdateDrainCanPeekStopWithoutConsumingNewTask(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "old", Kind: "voice"})
	event := fixtureEvent("2", "new-song", "filehelper", "incoming", "live")
	w.source = &fakeSource{events: []linuxnative.Event{event}, messages: map[string]linuxnative.Message{"new-song": fixtureMessage("new-song", "filehelper", "重新找一首，traveling light", "incoming")}}
	before := len(w.runtime.Inbox)
	w.peekVoiceInterruptions(context.Background(), "1", map[string]bool{})
	if n.cancels != 1 || len(w.runtime.Inbox) != before || n.sends != 1 || n.calls != 0 {
		t.Fatal("update either blocked stop or consumed a new model task")
	}
}
func TestMediaRetryUsesOriginalJobPreservesReceiptAndWaits(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "source", Kind: "voice", Title: "track.wav"})
	d := w.runtime.Media[0]
	n.status.State = "cancelled"
	n.status.Reason = "media_worker_stopping"
	n.status.Retryable = true
	n.status.VoiceChunks = []linuxnative.VoiceChunkStatus{{Index: 0, State: "accepted", HasReceipt: true, DurationMS: 40000}, {Index: 1, State: "prepared", DurationMS: 40000}}
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if d.State != "retry_wait" || n.retries != 0 || time.Until(d.Next) < 110*time.Second {
		t.Fatal("retry did not wait")
	}
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if n.retries != 0 {
		t.Fatal("only one check before retry")
	}
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if n.retries != 1 || n.sends != 1 || d.Status.VoiceChunks[0].State != "accepted" {
		t.Fatal("new job or accepted part replayed")
	}
	n.status.State = "accepted"
	n.status.ServerID = "123"
	n.status.VoiceChunks[1].State = "accepted"
	n.status.VoiceChunks[1].HasReceipt = true
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if d.State != "accepted" {
		t.Fatal("receipt missing")
	}
	scope := c.Store.Context(in, c.Config, time.Now())
	count := 0
	for _, turn := range scope.Messages {
		if turn.Media != nil {
			count++
			if turn.Media.AcceptedParts != 2 || turn.Media.DurationMS != 80000 {
				t.Fatal("chunk memory missing")
			}
		}
	}
	if count != 1 {
		t.Fatal("duplicate memory")
	}
}
func TestUncertainNativeGuardAndDrainDoNotResubmit(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "source", Kind: "voice"})
	d := w.runtime.Media[0]
	n.status.State = "needs_review"
	n.status.Retryable = false
	for i := 0; i < 3; i++ {
		d.Next = time.Time{}
		w.reconcileMedia(context.Background())
	}
	if d.State != "waiting" || n.retries != 0 || n.sends != 1 {
		t.Fatal("unsafe resubmit")
	}
	if w.pendingConversationThread(in.Thread) || w.pendingMedia() {
		t.Fatal("read-only uncertain receipt starved recovery tasks or updates")
	}
	w.config.Draining = func() bool { return true }
	w.suspendMedia()
	if n.cancels != 0 {
		t.Fatal("drain cancelled completed/reviewed job")
	}
	w.config.Draining = nil
	n.status.State = "accepted"
	n.status.ServerID = "123"
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if d.State != "accepted" || n.sends != 1 || n.retries != 0 {
		t.Fatal("late receipt was not reconciled", d)
	}
}
