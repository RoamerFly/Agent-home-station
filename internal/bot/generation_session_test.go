package bot

import (
	"context"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/agentrun"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestLiveFollowupBecomesOneDecisionAndOneDurableCompletion(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	steps := 0
	w, c, n := backgroundWorker(t, generationModelFunc(func(ctx context.Context, _ string, turns []map[string]string) (string, error) {
		steps++
		if steps == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return `{"action":"reply","messages":["stale answer"]}`, nil
		}
		joined := ""
		for _, v := range turns {
			joined += v["content"]
		}
		if !strings.Contains(joined, "current_followup") || !strings.Contains(joined, "只总结重点") {
			t.Error("followup missing")
		}
		return `{"action":"reply","messages":["new summary"]}`, nil
	}))
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "original", Mode: "direct", Text: "帮我总结"}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "original", "filehelper", "incoming", "remote"), Phase: "processing"}, {Event: fixtureEvent("2", "followup", "filehelper", "incoming", "remote"), Phase: "processing"}}
	w.startGeneration(context.Background(), c, in, nil)
	<-started
	m := fixtureMessage("followup", "filehelper", "只总结重点", "incoming")
	m.SenderID = "person"
	w.noticeConversationMessage(m, true)
	if !w.deferBusyGeneration(context.Background(), m) || w.runtime.Inbox[1].Phase != "following" {
		t.Fatal("followup independently queued")
	}
	w.reviewActiveGenerations(context.Background())
	if len(w.generation.reviews) != 0 {
		t.Fatal("extra task cancellation model was started")
	}
	close(release)
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if steps != 2 || len(n.submissions) != 0 || len(w.runtime.Plans) != 1 || w.runtime.Plans[0].Messages[0] != "new summary" || len(w.runtime.Plans[0].Updates) != 0 {
		t.Fatal("duplicate/stale turn", steps, w.runtime.Plans)
	}
	if item := w.runtime.Inbox[1]; item.Phase != "completed" || !item.FollowupConsumed || item.FollowupParent != "original" {
		t.Fatal(item)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	w.recoverFollowups()
	if w.runtime.Inbox[1].Phase != "completed" {
		t.Fatal("completed event replayed after restart")
	}
}

func TestLiveMediaFollowupReadsAttachmentAndKeepsRawXMLAwayFromModel(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	steps := 0
	w, c, _ := backgroundWorker(t, generationModelFunc(func(ctx context.Context, _ string, turns []map[string]string) (string, error) {
		steps++
		if steps == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return `{"action":"silent"}`, nil
		}
		all := ""
		for _, turn := range turns {
			all += turn["content"]
		}
		if !strings.Contains(all, "a visible cat") || !strings.Contains(all, "current_followup") || strings.Contains(all, "SYNTHETIC-ATTACHMENT-KEY") {
			t.Error("media followup missing observation or leaked raw payload")
		}
		return `{"action":"reply","messages":["看到了，猫在这里"]}`, nil
	}))
	a := &backgroundAnalyzer{make(chan struct{}), make(chan struct{})}
	close(a.release)
	w.config.Analyzer = a
	w.config.Options.ReadImages = true
	m := fixtureMessage("image-followup", "filehelper", "<img aeskey=\"SYNTHETIC-ATTACHMENT-KEY\"/>", "incoming")
	m.SenderID = "person"
	m.Type = 3
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "look", Mode: "direct", Text: "帮我看看这个"}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "look", "filehelper", "incoming", "remote"), Phase: "processing"}, {Event: fixtureEvent("2", m.ID, "filehelper", "incoming", "remote"), Phase: "processing"}}
	w.startGeneration(context.Background(), c, in, nil)
	<-started
	w.noticeConversationMessage(m, true)
	if w.runtime.Inbox[1].Phase != "following" {
		t.Fatal("media not captured")
	}
	close(release)
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if steps != 2 || w.runtime.Inbox[1].Phase != "completed" {
		t.Fatal("media not processed once", steps)
	}
}

func TestFollowupRestorationPreservesUnknownEffects(t *testing.T) {
	for _, toolStarted := range []bool{false, true} {
		w, _, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
		w.runtime.Inbox = []inboxItem{
			{Event: fixtureEvent("1", "parent", "filehelper", "incoming", "remote"), Phase: "tool_running", ToolStarted: toolStarted},
			{Event: fixtureEvent("2", "read", "filehelper", "incoming", "remote"), Phase: "following", FollowupParent: "parent", FollowupConsumed: true},
			{Event: fixtureEvent("3", "unread", "filehelper", "incoming", "remote"), Phase: "following", FollowupParent: "parent"},
		}
		if err := w.saveRuntime(); err != nil {
			t.Fatal(err)
		}
		if err := w.loadRuntime("server-1"); err != nil {
			t.Fatal(err)
		}
		w.recoverFollowups()
		want := "received"
		if toolStarted {
			want = "interrupted"
		}
		if w.runtime.Inbox[1].Phase != want || w.runtime.Inbox[2].Phase != "received" {
			t.Fatal("lost input or replayed unknown effect", w.runtime.Inbox)
		}
	}
}

func TestAnotherSpeakerAndUnreadFollowupRemainIndependent(t *testing.T) {
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "original"}
	j := &generationJob{input: in, engine: c, cancel: func() {}}
	j.session = w.liveSession(w.generation, j)
	w.generation.jobs[in.Thread] = j
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "member", "filehelper", "incoming", "remote"), Phase: "processing"}, {Event: fixtureEvent("2", "owner", "filehelper", "incoming", "remote"), Phase: "processing"}}
	m := fixtureMessage("member", "filehelper", "new topic", "incoming")
	m.SenderID = "member"
	w.noticeConversationMessage(m, true)
	if j.session.Pending() {
		t.Fatal("another speaker captured owner identity")
	}
	m.ID = "owner"
	m.SenderID = "owner"
	w.noticeConversationMessage(m, true)
	if !j.session.Pending() {
		t.Fatal("same speaker not captured")
	}
	w.settleGenerationFollowups(j, false)
	if w.runtime.Inbox[1].Phase != "received" {
		t.Fatal("unread followup dropped")
	}
	delete(w.generation.jobs, in.Thread)
}

func TestLargeSessionBoundaryKeepsFollowupActiveWhenPruning(t *testing.T) {
	if !inboxActivePhase("following") {
		t.Fatal("active followup eligible for pruning")
	}
	s := agentrun.New(agentrun.Scope{Account: "a", Thread: "t", Sender: "s", RunID: "r"}, nil, nil)
	s.Offer("a", agentrun.Event{ID: "1", Thread: "t", Sender: "s", At: time.Now().Format(time.RFC3339)})
	if s.Seal() {
		t.Fatal("pending followup sealed")
	}
}
