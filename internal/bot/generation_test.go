package bot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type generationModelFunc func(context.Context, string, []map[string]string) (string, error)

func TestGenerationCancelRequiresAnActualNewMessageAndReason(t *testing.T) {
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
	w.enableGenerations()
	defer w.closeGenerations()
	cancelled := false
	j := &generationJob{input: companion.Input{Thread: "filehelper", TraceID: "birthday"}, engine: c, cancel: func() { cancelled = true }, updates: []companion.ConversationEvent{{MessageID: "offline", Text: "机器人还没开机"}}}
	w.generation.jobs[j.input.Thread] = j
	for _, d := range []companion.PendingReview{{Action: "cancel"}, {Action: "cancel", EvidenceMessageID: "invented", Reason: "cancel"}} {
		w.finishGenerationReview(planReviewResult{generation: j, fingerprint: generationFingerprint(j), decision: d})
		if cancelled || j.cancelled {
			t.Fatal("unsubstantiated cancellation stopped request")
		}
	}
	delete(w.generation.jobs, j.input.Thread)
}

func (f generationModelFunc) Chat(ctx context.Context, s string, m []map[string]string) (string, error) {
	return f(ctx, s, m)
}
func backgroundWorker(t *testing.T, model companion.LanguageModel) (*Worker, *companion.Engine, *acceptingProgressNative) {
	t.Helper()
	w, c, n, _ := planWorker(t, &planReviewModel{})
	// Background fixtures supply their own originals and sender identities;
	// do not reuse planWorker's unrelated message with ID "original".
	store, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.Observe(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "background-fixture-source", Text: "合成历史：下次继续聊天。"}, c.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(c.Config, store, model, "test")
	if err != nil {
		t.Fatal(err)
	}
	engine.Config.Conversation.AggregateMS = 0
	w.model = engine
	w.config.PollInterval = time.Millisecond
	return w, engine, n
}

// Direct scheduler tests bypass the native inbox adapter. Supply the same
// authenticated message metadata that adapter records, rather than testing
// transport with anonymous, untimed drafts no real sender could produce.
func (w *Worker) startSyntheticGeneration(t *testing.T, ctx context.Context, e *companion.Engine, in companion.Input, analysis *linuxnative.Message) {
	t.Helper()
	if in.Sender == "" && in.Mode != "proactive" && !in.Surf {
		in.Sender = "synthetic-requester"
	}
	if in.Sender != "" && in.Mode != "proactive" && !in.Surf {
		m := fixtureMessage(in.TraceID, in.Thread, in.Text, "incoming")
		m.SenderID = in.Sender
		if analysis != nil {
			m = *analysis
			if m.AccountID == "" {
				m.AccountID = in.Account
			}
			if m.ThreadID == "" {
				m.ThreadID = in.Thread
			}
			if m.SenderID == "" {
				m.SenderID = in.Sender
			}
			if m.CreatedAt == "" {
				m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
			}
			analysis = &m
		}
		if m.ServerID == "" {
			m.ServerID = "synthetic-native-" + m.ID
		}
		if in.Source == nil || in.Source.Thread != in.Thread || in.Source.MessageID != in.TraceID {
			bindInputSource(&in, m, e)
		}
		if in.MessageTime == "" {
			in.MessageTime = m.CreatedAt
		}
		if in.OriginalText == nil {
			original := m.Body
			if m.Type != 1 {
				original = ""
			}
			in.OriginalText = &original
		}
		if err := e.Store.Observe(in, e.Config, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	w.startGeneration(ctx, e, in, analysis)
}
func pumpGeneration(t *testing.T, w *Worker, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for !done() && ctx.Err() == nil {
		w.drainGenerations(ctx)
		w.waitGeneration(ctx, time.Millisecond)
	}
	if !done() {
		t.Fatal("background work did not complete")
	}
	if w.persistErr != nil {
		t.Fatal(w.persistErr)
	}
}

func TestDurableInboxContinuesWhileAnotherConversationModelWaits(t *testing.T) {
	started, exited := make(chan struct{}), make(chan struct{})
	model := generationModelFunc(func(ctx context.Context, _ string, m []map[string]string) (string, error) {
		if strings.Contains(m[len(m)-1]["content"], "review_unsent_message_plan") || strings.Contains(m[len(m)-1]["content"], "review_running_task") {
			return `{"action":"continue"}`, nil
		}
		if strings.Contains(m[len(m)-1]["content"], "slow-request") {
			close(started)
			<-ctx.Done()
			close(exited)
			return "", ctx.Err()
		}
		return `{"action":"reply","messages":["independent-result"]}`, nil
	})
	w, _, n := backgroundWorker(t, model)
	slow := fixtureMessage("slow", "filehelper", "/ai slow-request", "incoming")
	slow.SenderID = "person"
	friend := fixtureMessage("friend", "friend", "/ai independent-request", "incoming")
	friend.SenderID = "friend-person"
	interrupt := fixtureMessage("interrupt", "filehelper", "/ai 先不用讲了", "incoming")
	interrupt.SenderID = "person"
	s := &fakeSource{events: []linuxnative.Event{fixtureEvent("1", "slow", "filehelper", "incoming", "remote")}, messages: map[string]linuxnative.Message{"slow": slow, "friend": friend, "interrupt": interrupt}}
	added := false
	s.onPage = func(string) {
		select {
		case <-started:
			if !added {
				s.events = append(s.events, fixtureEvent("2", "friend", "friend", "incoming", "remote"), fixtureEvent("3", "interrupt", "filehelper", "incoming", "remote"))
				added = true
			}
		default:
		}
	}
	w.source = s
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	received := map[string]int{}
	w.config.Record = func(r Record) {
		if r.Event == "message_received" {
			received[r.MessageID]++
		}
		if r.Event == "message_sent" && r.ThreadID == "friend" {
			cancel()
		}
	}
	state := checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "0"}
	if err := w.runDurable(ctx, filepath.Join(w.config.StateDir, "checkpoint.json"), state); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("shutdown left model alive")
	}
	if len(n.submissions) != 1 || n.submissions[0].ThreadID != "friend" {
		t.Fatal("slow generation blocked independent send", n.submissions)
	}
	if received["interrupt"] != 1 || received["friend"] != 1 {
		t.Fatal("new inbox event lost or repeatedly logged", received)
	}
	if len(w.generation.jobs["filehelper"].updates) != 1 {
		t.Fatal("interruption not retained during generation")
	}
	for _, item := range w.runtime.Inbox {
		if item.Event.Data.MessageID == "interrupt" && item.Phase != "received" && item.Phase != "following" {
			t.Fatal("deferred same-requester update was marked completed", item)
		}
	}
}

type blockingGenerationTools struct{ entered, exited chan struct{} }

func (t *blockingGenerationTools) ForkTools() companion.ToolAccess {
	return &blockingGenerationTools{t.entered, t.exited}
}
func (t *blockingGenerationTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "web_media_download"}}, nil
}
func (t *blockingGenerationTools) Call(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
	close(t.entered)
	<-ctx.Done()
	close(t.exited)
	return "", ctx.Err()
}
func TestNewFileInstructionCancelsInFlightVoiceDownloadWithoutPublishingItsResult(t *testing.T) {
	model := generationModelFunc(func(_ context.Context, _ string, _ []map[string]string) (string, error) {
		return `{"action":"tool","tool":"web_media_download","arguments":{"url":"https://example.org/song.mp3","kind":"voice"}}`, nil
	})
	w, c, n := backgroundWorker(t, model)
	tools := &blockingGenerationTools{make(chan struct{}), make(chan struct{})}
	c.Tools = tools
	c.MaxToolCalls = 1
	w.enableGenerations()
	defer w.closeGenerations()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "song", Mode: "direct", Directed: true, Text: "听这个 https://example.org/song.mp3"}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "song", "filehelper", "incoming", "remote"), Phase: "processing"}}
	w.startSyntheticGeneration(t, ctx, c, in, nil)
	pumpGeneration(t, w, func() bool {
		select {
		case <-tools.entered:
			return true
		default:
			return false
		}
	})
	if !w.runtime.Inbox[0].ToolStarted || w.runtime.Inbox[0].Phase != "tool_running" {
		t.Fatal("tool side effect was not marked before execution")
	}
	stop := fixtureMessage("file-request", "filehelper", "/ai 改发文件，不用发语音", "incoming")
	stop.SenderID = "synthetic-requester"
	w.noticeConversationMessage(stop, true)
	if !w.deferBusyGeneration(ctx, stop) {
		t.Fatal("old work was not deferred")
	}
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	select {
	case <-tools.exited:
	default:
		t.Fatal("download did not receive cancellation")
	}
	if len(n.submissions) != 0 || len(w.runtime.Plans) != 0 || w.runtime.Inbox[0].Phase != "interrupted" {
		t.Fatal("cancelled generation published stale media or text")
	}
}

func TestSlowPlanReviewAllowsAnotherThreadAndDiscardsObsoleteReview(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	model := generationModelFunc(func(ctx context.Context, _ string, _ []map[string]string) (string, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
			}
			return `{"action":"replace","messages":["obsolete replacement"]}`, nil
		}
		return `{"action":"cancel"}`, nil
	})
	w, c, n := backgroundWorker(t, model)
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "old", Mode: "direct", Text: "聊一聊"}
	in = observeSyntheticMediaRequest(t, w, c, in)
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"old pending"}})
	updateOne := fixtureMessage("update-one", "filehelper", "简单说", "incoming")
	updateOne.SenderID = in.Sender
	w.noticeConversationMessage(updateOne, true)
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("review not started")
	}
	other := in
	other.Thread = "friend"
	other.TraceID = "other"
	other = observeSyntheticMediaRequest(t, w, c, other)
	w.queueUtterancePlan(c, other, companion.Decision{Messages: []string{"independent text"}})
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 1 || n.submissions[0].ThreadID != "friend" {
		t.Fatal("review blocked another conversation")
	}
	updateTwo := fixtureMessage("update-two", "filehelper", "先别说了", "incoming")
	updateTwo.SenderID = in.Sender
	w.noticeConversationMessage(updateTwo, true)
	close(release)
	pumpGeneration(t, w, func() bool { return len(w.generation.reviews) == 0 })
	p := w.runtime.Plans[0]
	if p.Messages[0] != "old pending" || p.State != "review_pending" {
		t.Fatal("obsolete review overwrote newer context", p)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	pumpGeneration(t, w, func() bool { return len(w.generation.reviews) == 0 })
	if p.State != "stopped" || len(n.submissions) != 1 {
		t.Fatal("latest stop was ignored", p)
	}
}

func TestModelCanCancelAnInFlightTaskFromNewContextWithoutStopKeywords(t *testing.T) {
	started := make(chan struct{})
	model := generationModelFunc(func(ctx context.Context, _ string, m []map[string]string) (string, error) {
		if strings.Contains(m[len(m)-1]["content"], "review_unsent_message_plan") || strings.Contains(m[len(m)-1]["content"], "review_running_task") {
			return `{"action":"cancel","evidence_message_id":"changed","reason":"requester changed their request"}`, nil
		}
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	w, c, n := backgroundWorker(t, model)
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "search", Mode: "direct", Text: "帮我整理一些资料"}
	w.startSyntheticGeneration(t, context.Background(), c, in, nil)
	// Exercise the compatibility review path for a task without a live session.
	w.generation.jobs[in.Thread].session = nil
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	changed := fixtureMessage("changed", "filehelper", "我已经找到答案了，咱们聊点别的吧", "incoming")
	changed.SenderID = "synthetic-requester"
	w.noticeConversationMessage(changed, true)
	w.reviewActiveGenerations(context.Background())
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if len(n.submissions) != 0 || len(w.runtime.Plans) != 0 {
		t.Fatal("superseded work still published")
	}
}

func TestCompletedGenerationIsHeldUntilFreshInboxAndInterruptionReview(t *testing.T) {
	model := generationModelFunc(func(_ context.Context, _ string, m []map[string]string) (string, error) {
		if strings.Contains(m[len(m)-1]["content"], "review_unsent_message_plan") || strings.Contains(m[len(m)-1]["content"], "review_running_task") {
			return `{"action":"cancel","evidence_message_id":"updated","reason":"request already solved"}`, nil
		}
		return `{"action":"reply","messages":["old result"]}`, nil
	})
	w, c, n := backgroundWorker(t, model)
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "ready", Mode: "direct", Text: "旧请求"}
	w.startSyntheticGeneration(t, context.Background(), c, in, nil)
	// A saved/sessionless task must still use the existing interruption review.
	w.generation.jobs[in.Thread].session = nil
	deadline := time.Now().Add(time.Second)
	for len(w.generation.ready) == 0 && time.Now().Before(deadline) {
		w.waitGeneration(context.Background(), time.Millisecond)
	}
	if len(w.generation.ready) != 1 || len(w.runtime.Plans) != 0 {
		t.Fatal("result published ahead of fresh inbox scan")
	}
	updated := fixtureMessage("updated", "filehelper", "那个问题已经解决了", "incoming")
	updated.SenderID = "synthetic-requester"
	w.noticeConversationMessage(updated, true)
	w.reviewActiveGenerations(context.Background())
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if len(n.submissions) != 0 || len(w.runtime.Plans) != 0 {
		t.Fatal("ready result ignored newer message")
	}
}

func TestMediaCaptionJoinsInterruptiblePlanOnlyAfterAttachmentReceipt(t *testing.T) {
	w, c, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"cancel"}`, nil }))
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "media", Mode: "direct", Text: "发这首歌"}
	in = observeSyntheticMediaRequest(t, w, c, in)
	w.deferMediaReply(in, []string{"找到这首歌了", "要原音频文件吗？"}, 1)
	w.runtime.Media = []*mediaDelivery{{Trace: in.TraceID, Input: in, Kind: "voice", State: "running"}, {Trace: "independent-file", Input: in, Kind: "file", State: "running"}}
	w.reconcileMediaReplies(context.Background())
	if len(w.runtime.Plans) != 0 {
		t.Fatal("caption preceded receipt")
	}
	w.runtime.Media[0].State = "accepted"
	w.reconcileMediaReplies(context.Background())
	if len(w.runtime.Plans) != 1 || !w.runtime.Plans[0].MediaCaption || len(n.submissions) != 0 {
		t.Fatal("caption did not join semantic queue")
	}
	update := fixtureMessage("new-file", "filehelper", "不用介绍了，直接给我原文件吧", "incoming")
	update.SenderID = in.Sender
	w.noticeConversationMessage(update, true)
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	pumpGeneration(t, w, func() bool { return len(w.generation.reviews) == 0 })
	if w.runtime.Plans[0].State != "stopped" || w.runtime.Media[0].State != "accepted" || w.runtime.Media[1].State != "running" || len(n.submissions) != 0 {
		t.Fatal("caption cancellation changed independent attachment state")
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal("caption handoff cannot survive restart", err)
	}
	if !w.runtime.MediaReplies[0].terminal() || w.runtime.Plans[0].State != "stopped" {
		t.Fatal("delegated caption revived after restart")
	}
}

func TestGenerationRestartReevaluatesPureModelButNeverReplaysStartedTools(t *testing.T) {
	for _, toolStarted := range []bool{false, true} {
		t.Run(map[bool]string{false: "pure-model", true: "started-tool"}[toolStarted], func(t *testing.T) {
			var calls atomic.Int32
			w, _, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
				calls.Add(1)
				return `{"action":"reply","messages":["recovered"]}`, nil
			}))
			w.source = &fakeSource{messages: map[string]linuxnative.Message{"restart": fixtureMessage("restart", "filehelper", "/ai restart-request", "incoming")}}
			w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "restart", "filehelper", "incoming", "remote"), Phase: "model_running", ToolStarted: toolStarted}}
			if err := w.saveRuntime(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			w.config.Record = func(r Record) {
				if r.Event == "message_sent" || r.Event == "tool_task_interrupted" {
					cancel()
				}
			}
			if err := w.runDurable(ctx, filepath.Join(w.config.StateDir, "checkpoint.json"), checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "1"}); err != nil {
				t.Fatal(err)
			}
			if toolStarted {
				if calls.Load() != 0 || len(n.submissions) != 0 || w.runtime.Inbox[0].Phase != "interrupted" {
					t.Fatal("started tool was replayed")
				}
			} else {
				if calls.Load() != 1 || len(n.submissions) != 1 {
					t.Fatal("pure generation was lost instead of reevaluated")
				}
			}
		})
	}
}

func TestAsyncSurfKeepsExplicitQuietDeadlineWithoutImmediateCancellation(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	w, c, n := backgroundWorker(t, generationModelFunc(func(ctx context.Context, _ string, _ []map[string]string) (string, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return "", ctx.Err()
	}))
	c.Config.Permissions.Proactive = true
	w.enableGenerations()
	defer w.closeGenerations()
	w.config.SurfTask = func(time.Time) (*SurfTask, error) {
		return &SurfTask{Thread: "filehelper", Topic: "海洋", QuietDeadline: time.Now().Add(500 * time.Millisecond)}, nil
	}
	if !w.tickSurf(context.Background()) {
		t.Fatal("surf did not start")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("surf model did not start")
	}
	select {
	case <-stopped:
		t.Fatal("returning from scheduler cancelled surf immediately")
	default:
	}
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	select {
	case <-stopped:
	default:
		t.Fatal("quiet boundary did not cancel model")
	}
	if len(n.submissions) != 0 {
		t.Fatal("quiet-boundary task sent a message")
	}
}

type backgroundAnalyzer struct{ started, release chan struct{} }

func TestMediaCaptionCannotCancelUnreadObservation(t *testing.T) {
	var reviews atomic.Int32
	model := generationModelFunc(func(_ context.Context, _ string, messages []map[string]string) (string, error) {
		data := messages[len(messages)-1]["content"]
		if strings.Contains(data, "review_unsent_message_plan") || strings.Contains(data, "review_running_task") {
			reviews.Add(1)
			if !strings.Contains(data, "a visible cat") || !strings.Contains(data, "早餐") {
				return `{"action":"cancel","reason":"new topic","evidence_message_id":"caption"}`, nil
			}
			return `{"action":"continue"}`, nil
		}
		joined := ""
		for _, turn := range messages {
			joined += turn["content"]
		}
		if !strings.Contains(joined, "a visible cat") || !strings.Contains(joined, "早餐") || !strings.Contains(joined, "current_followup") {
			t.Error("main decision did not receive observation and subsequent caption")
		}
		return `{"action":"reply","messages":["图里的猫也想吃早餐吗"]}`, nil
	})
	w, c, _ := backgroundWorker(t, model)
	a := &backgroundAnalyzer{make(chan struct{}), make(chan struct{})}
	w.config.Analyzer = a
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "image", Mode: "direct", Text: "收到了一张图片"}
	w.startSyntheticGeneration(t, context.Background(), c, in, &linuxnative.Message{ID: "image", Type: 3})
	<-a.started
	caption := fixtureMessage("caption", "filehelper", "早餐", "incoming")
	caption.SenderID = "person"
	w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent("2", "caption", "filehelper", "incoming", "remote"), Phase: "processing"})
	w.noticeGenerationMessage(caption, true)
	w.reviewActiveGenerations(context.Background())
	if len(w.generation.reviews) != 0 || reviews.Load() != 0 {
		t.Fatal("review attempted to replace unread media")
	}
	close(a.release)
	pumpGeneration(t, w, func() bool { return !w.generation.jobs[in.Thread].observationPending })
	j := w.generation.jobs[in.Thread]
	if !strings.Contains(j.input.Text, "a visible cat") {
		t.Fatal("review still sees placeholder")
	}
	w.reviewActiveGenerations(context.Background())
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if j.cancelled || reviews.Load() != 0 || len(w.runtime.Plans) != 1 || w.runtime.Inbox[0].Phase != "completed" {
		t.Fatal("caption discarded understood media", j.cancelled, reviews.Load(), len(w.runtime.Plans))
	}
}

func TestMediaReadFailureWithQueuedCaptionDoesNotWaitForImpossibleReview(t *testing.T) {
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Thread: "filehelper", TraceID: "failed-image", Text: "image", Mode: "direct"}
	j := &generationJob{input: in, engine: c, observationPending: true, cancel: func() {}, updates: []companion.ConversationEvent{{MessageID: "caption", Text: "早餐"}}}
	w.generation.jobs[in.Thread] = j
	w.finishGeneration(context.Background(), generationResult{job: j, engine: c, input: in, err: context.DeadlineExceeded})
	if len(w.generation.jobs) != 0 || len(w.generation.ready) != 0 {
		t.Fatal("failed observation stuck behind review barrier")
	}
}

func TestAnotherGroupMemberCannotCancelOriginalRequest(t *testing.T) {
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
	w.enableGenerations()
	defer w.closeGenerations()
	cancelled := false
	j := &generationJob{input: companion.Input{Thread: "filehelper", TraceID: "image", Sender: "person"}, engine: c, cancel: func() { cancelled = true }, updates: []companion.ConversationEvent{{MessageID: "other", Sender: "someone-else", Text: "聊点别的"}}}
	w.generation.jobs[j.input.Thread] = j
	w.finishGenerationReview(planReviewResult{generation: j, fingerprint: generationFingerprint(j), decision: companion.PendingReview{Action: "cancel", EvidenceMessageID: "other", Reason: "changed topic"}})
	if cancelled || j.cancelled {
		t.Fatal("another person's topic cancelled media")
	}
}

func (a *backgroundAnalyzer) Analyze(ctx context.Context, _ linuxnative.Message) (string, error) {
	close(a.started)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-a.release:
		return "a visible cat", nil
	}
}
func TestMediaUnderstandingDoesNotBlockAnotherConversation(t *testing.T) {
	w, c, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		return `{"action":"reply","messages":["finished"]}`, nil
	}))
	a := &backgroundAnalyzer{make(chan struct{}), make(chan struct{})}
	w.config.Analyzer = a
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Thread: "filehelper", TraceID: "image", Mode: "direct", Text: "看图片"}
	w.startSyntheticGeneration(t, context.Background(), c, in, &linuxnative.Message{ID: "image", Type: 3})
	select {
	case <-a.started:
	case <-time.After(time.Second):
		t.Fatal("analysis did not start")
	}
	other := in
	other.Thread = "friend"
	other.TraceID = "friend"
	w.startSyntheticGeneration(t, context.Background(), c, other, nil)
	pumpGeneration(t, w, func() bool { return w.generation.jobs["friend"] == nil })
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 1 || n.submissions[0].ThreadID != "friend" {
		t.Fatal("media analyzer blocked another thread")
	}
	close(a.release)
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
}

type forkProgressTools struct{ checkedProgressTools }

func (t *forkProgressTools) ForkTools() companion.ToolAccess {
	return &forkProgressTools{checkedProgressTools: checkedProgressTools{before: t.before}}
}
func TestBackgroundProgressHasReceiptAndPreservesModelFinalSilence(t *testing.T) {
	w, c, n := backgroundWorker(t, &progressSequence{finalSilent: true})
	var toolCalls atomic.Int32
	c.Tools = &forkProgressTools{checkedProgressTools: checkedProgressTools{before: func() { toolCalls.Add(1) }}}
	c.MaxToolCalls = 3
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "progress", Mode: "direct", Text: "查一下这个来源"}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "progress", "filehelper", "incoming", "remote"), Phase: "processing"}}
	w.startSyntheticGeneration(t, context.Background(), c, in, nil)
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if toolCalls.Load() != 2 || len(n.submissions) != 1 || !strings.HasSuffix(n.submissions[0].Text, "收到，我看看～") {
		t.Fatal("progress missing, duplicated, or tool not continued", n.submissions)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 1 {
		t.Fatal("program added a fixed final outcome", n.submissions)
	}
	scope := c.Store.Context(in, c.Config, time.Now())
	count := 0
	for _, m := range scope.Messages {
		if strings.HasSuffix(m.Text, "收到，我看看～") {
			count++
		}
	}
	if count != 1 {
		t.Fatal("progress memory duplicate", count)
	}
}

func TestUpdateDrainWaitsForActiveGenerationAndPreservesNewInbox(t *testing.T) {
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		close(started)
		<-release
		close(finished)
		return `{"action":"reply","messages":["ready after drain"]}`, nil
	}))
	_ = c
	s := &fakeSource{events: []linuxnative.Event{fixtureEvent("1", "running", "filehelper", "incoming", "remote")}, messages: map[string]linuxnative.Message{"running": fixtureMessage("running", "filehelper", "/ai long task", "incoming")}}
	w.source = s
	w.config.Draining = func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}
	s.onPage = func(string) {
		select {
		case <-started:
			if len(s.events) == 1 {
				s.events = append(s.events, fixtureEvent("2", "next", "friend", "incoming", "remote"))
			}
		default:
		}
	}
	go func() { <-started; time.Sleep(30 * time.Millisecond); close(release) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.runDurable(ctx, filepath.Join(w.config.StateDir, "checkpoint.json"), checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "0"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("update cancelled generation")
	}
	if ctx.Err() != nil || len(w.runtime.Plans) != 1 || w.runtime.Plans[0].Messages[0] != "ready after drain" {
		t.Fatal("finished task not persisted for replacement worker")
	}
	for _, item := range w.runtime.Inbox {
		if item.Event.Data.MessageID == "next" && item.Phase != "received" {
			t.Fatal("drain consumed next request")
		}
	}
}

func TestAuthorizedImageUsesIndependentMediaQueueBeforeCaption(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	c.Config.Permissions.SendImage = true
	c.Images = []companion.ImageAsset{{ID: "saved-image", Title: "已授权图片"}}
	w.enableGenerations()
	defer w.closeGenerations()
	w.finishCharacter(context.Background(), c, in, companion.Decision{Action: "reply", ImageID: "saved-image", Messages: []string{"这张图很可爱"}}, nil, nil, false, false)
	if len(w.runtime.Media) != 1 || w.runtime.Media[0].Kind != "image" || len(w.runtime.MediaReplies) != 1 || n.calls != 0 {
		t.Fatal("image still used blocking send or caption preceded receipt")
	}
}

type pagedGenerationSource struct{ fakeSource }

func (s *pagedGenerationSource) Events(ctx context.Context, cursor string) (linuxnative.EventPage, error) {
	p, e := s.fakeSource.Events(ctx, cursor)
	if len(p.Items) > 100 {
		p.Items = p.Items[:100]
		p.NextCursor = p.Items[99].Cursor
	}
	return p, e
}
func TestOldResultWaitsForStopRequestOnNextInboxPage(t *testing.T) {
	w, c, n := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, m []map[string]string) (string, error) {
		if strings.Contains(m[len(m)-1]["content"], "review_unsent_message_plan") || strings.Contains(m[len(m)-1]["content"], "review_running_task") {
			return `{"action":"cancel"}`, nil
		}
		return `{"action":"silent"}`, nil
	}))
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "old-result", Mode: "direct", Text: "旧请求"}
	in = observeSyntheticMediaRequest(t, w, c, in)
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"不该抢先发出的旧内容"}})
	s := &pagedGenerationSource{fakeSource{messages: map[string]linuxnative.Message{}}}
	for i := 1; i <= 100; i++ {
		id := strconv.Itoa(i)
		s.events = append(s.events, fixtureEvent(id, id, "unrelated", "incoming", "remote"))
		s.messages[id] = fixtureMessage(id, "unrelated", "noise", "incoming")
	}
	s.events = append(s.events, fixtureEvent("101", "stop", "filehelper", "incoming", "remote"))
	s.messages["stop"] = fixtureMessage("stop", "filehelper", "/ai 不用再说了", "incoming")
	stop := s.messages["stop"]
	stop.SenderID = in.Sender
	s.messages["stop"] = stop
	w.source = s
	// The first page requires 100 durable writes. End on the cancellation
	// event below, with a watchdog independent of normal disk latency.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	w.config.Record = func(r Record) {
		if r.Event == "utterance_plan_revised" && r.Reason == "plan_cancel" {
			cancel()
		}
	}
	if err := w.runDurable(ctx, filepath.Join(w.config.StateDir, "checkpoint.json"), checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "0"}); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("worker never processed the queued stop on the second page")
	}
	if len(n.submissions) != 0 || w.runtime.Plans[0].State != "stopped" {
		t.Fatal("page-one scan sent old result before queued stop on page two", n.submissions)
	}
}

func TestAsyncWakeRunsWithoutNewQuestionAndDoesNotReplayAfterRestart(t *testing.T) {
	var calls atomic.Int32
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		calls.Add(1)
		return `{"action":"silent"}`, nil
	}))
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{"filehelper"}
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "prior-plan", Mode: "direct", Text: "下次再聊"}
	w.scheduleConversationWake(c, in, 30, true)
	if len(w.runtime.Wakes) != 1 {
		t.Fatal("wake not scheduled")
	}
	w.runtime.Wakes[0].Due = time.Now().Add(-time.Second)
	if !w.tickConversationWake(context.Background()) || w.runtime.Wakes[0].State != "running" {
		t.Fatal("wake did not enter background job")
	}
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if calls.Load() != 1 || w.runtime.Wakes[0].State != "completed" {
		t.Fatal("wake requires an incoming question or did not finish")
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	if w.tickConversationWake(context.Background()) || calls.Load() != 1 {
		t.Fatal("completed wake replayed after restart")
	}
}

func TestPendingPlanDelayAndContinueRetainUnsentContentWithoutBlockingOtherThread(t *testing.T) {
	for _, action := range []string{"delay", "continue"} {
		t.Run(action, func(t *testing.T) {
			w, c, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
				return `{"action":"` + action + `","delay_seconds":30}`, nil
			}))
			w.enableGenerations()
			defer w.closeGenerations()
			in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "old", Mode: "direct", Text: "旧话题"}
			in = observeSyntheticMediaRequest(t, w, c, in)
			if err := c.Store.Observe(in, c.Config, time.Now()); err != nil {
				t.Fatal(err)
			}
			w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"需要保留的未发内容"}})
			update := fixtureMessage("update", "filehelper", "稍等一下再说", "incoming")
			update.SenderID = in.Sender
			w.noticeConversationMessage(update, true)
			readyPlans(w)
			w.reconcileUtterancePlans(context.Background())
			pumpGeneration(t, w, func() bool { return len(w.generation.reviews) == 0 })
			p := w.runtime.Plans[0]
			if p.State != "queued" || len(p.Updates) != 0 || p.Messages[0] != "需要保留的未发内容" {
				t.Fatal("review lost pending content")
			}
			if action == "delay" && time.Until(p.Next) < 25*time.Second {
				t.Fatal("model delay not enforced")
			}
			if action == "continue" {
				p.Next = time.Now().Add(30 * time.Second)
			}
			other := in
			other.Thread = "friend"
			other.TraceID = "other"
			other = observeSyntheticMediaRequest(t, w, c, other)
			w.queueUtterancePlan(c, other, companion.Decision{Messages: []string{"独立会话"}})
			w.runtime.Plans[1].InboxChecked = true
			w.reconcileUtterancePlans(context.Background())
			if len(n.submissions) != 1 || n.submissions[0].ThreadID != "friend" {
				t.Fatal("paused plan blocked another thread")
			}
			p.Next = time.Time{}
			w.reconcileUtterancePlans(context.Background())
			if len(n.submissions) != 2 || !strings.Contains(n.submissions[1].Text, "需要保留") {
				t.Fatal("resumed plan lost original content", n.submissions, p)
			}
		})
	}
}
