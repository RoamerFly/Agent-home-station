package bot

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func TestBackgroundReasoningYieldsToNewPrivateQuestionWithoutLosingInbox(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	w, c, n := backgroundWorker(t, generationModelFunc(func(ctx context.Context, _ string, _ []map[string]string) (string, error) {
		if calls.Add(1) > 1 {
			return `{"action":"reply","messages":["我核对了原任务结果"]}`, nil
		}
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}))
	w.enableGenerations()
	defer w.closeGenerations()
	done := ""
	w.config.AgentTaskDone = func(in companion.Input, state string) {
		if in.TaskID != "saved-process" {
			t.Error(in)
		}
		done = state
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "process-result", TaskID: "saved-process", Mode: "direct", Text: "Inspect original process output"}
	w.startSyntheticGeneration(t, context.Background(), c, in, nil)
	<-entered
	w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent("2", "status-question", "filehelper", "incoming", "remote"), Phase: "processing"})
	message := fixtureMessage("status-question", "filehelper", "好了吗", "incoming")
	message.SenderID = "person"
	w.noticeGenerationMessage(message, true)
	if !w.deferBusyGeneration(context.Background(), message) {
		t.Fatal("must retain new request while old run exits")
	}
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if done != "yielded_to_message" || w.runtime.Inbox[0].Phase != "received" || w.runtime.Inbox[0].FollowupConsumed || len(n.submissions) != 0 || len(w.runtime.Plans) != 0 {
		t.Fatal("background run swallowed request or produced stale response", done, w.runtime.Inbox)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	if w.runtime.Inbox[0].Phase != "received" {
		t.Fatal("question not durable after restart")
	}
	question := in
	question.TaskID = ""
	question.TraceID = "status-question"
	question.Text = "好了吗"
	w.startSyntheticGeneration(t, context.Background(), c, question, nil)
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if len(w.runtime.Plans) != 1 || w.runtime.Plans[0].Messages[0] != "我核对了原任务结果" || calls.Load() != 2 {
		t.Fatal("new foreground question never reached its own model decision", w.runtime.Plans)
	}
}

type boundaryTools struct {
	entered, release chan struct{}
	calls            int
	cancelled        bool
}

func (b *boundaryTools) ForkTools() companion.ToolAccess { return b }
func (b *boundaryTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "server_exec"}}, nil
}
func (b *boundaryTools) Call(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
	b.calls++
	close(b.entered)
	select {
	case <-ctx.Done():
		b.cancelled = true
		return "", ctx.Err()
	case <-b.release:
		return `{"exit_code":0,"evidence":"already executed"}`, nil
	}
}

func TestBackgroundYieldWaitsForActualToolBoundaryWithoutReplay(t *testing.T) {
	b := &boundaryTools{entered: make(chan struct{}), release: make(chan struct{})}
	w, c, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		return `{"action":"tool","tool":"server_exec","arguments":{"command":"synthetic"}}`, nil
	}))
	c.Tools = b
	c.MaxToolCalls = 10
	w.enableGenerations()
	defer w.closeGenerations()
	done := ""
	w.config.AgentTaskDone = func(_ companion.Input, state string) { done = state }
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "old-task", TaskID: "saved-task", Mode: "direct", Text: "saved repair"}
	w.startSyntheticGeneration(t, context.Background(), c, in, nil)
	pumpGeneration(t, w, func() bool {
		select {
		case <-b.entered:
			return true
		default:
			return false
		}
	})
	if !w.generation.jobs[in.Thread].toolInFlight {
		t.Fatal("tool boundary not tracked")
	}
	message := fixtureMessage("new-question", "filehelper", "进度呢", "incoming")
	message.SenderID = "person"
	w.noticeGenerationMessage(message, true)
	if b.cancelled || w.generation.jobs[in.Thread].cancelled {
		t.Fatal("yield killed a real operation")
	}
	close(b.release)
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	if b.calls != 1 || b.cancelled || done != "yielded_to_message" || len(n.submissions) > 0 {
		t.Fatal("tool replayed/cancelled or old output sent", b.calls, done)
	}
}

func TestUndirectedMessageDoesNotYieldBackgroundTask(t *testing.T) {
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
	w.enableGenerations()
	defer w.closeGenerations()
	cancelled := false
	j := &generationJob{input: companion.Input{Thread: "filehelper", TaskID: "saved-task", TraceID: "old"}, engine: c, cancel: func() { cancelled = true }}
	w.generation.jobs["filehelper"] = j
	w.noticeGenerationMessage(fixtureMessage("ordinary", "filehelper", "ambient", "incoming"), false)
	if cancelled || j.yieldRequested {
		t.Fatal("ineligible event interrupted saved task")
	}
	delete(w.generation.jobs, "filehelper")
}

func TestDirectedBackgroundTaskMaySendModelAuthoredProgress(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "synchronous", true: "generation"}[background], func(t *testing.T) {
			w, c, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
			c.Config.ReplyMinMS = 0
			c.Config.ReplyMaxMS = 0
			c.Config.ShowAILabel = false
			w.config.Options.RandomDelay = false
			w.config.Options.AcknowledgeTools = true
			in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "progress", TaskID: "saved-task", Mode: "direct", Text: "continue original request"}
			in = observeSyntheticMediaRequest(t, w, c, in)
			if !background {
				sent, err := w.toolProgress(c, in)(context.Background(), "我去核对一下刚才的结果～")
				if err != nil || !sent {
					t.Fatal(sent, err)
				}
			} else {
				w.enableGenerations()
				defer w.closeGenerations()
				result := make(chan bool, 1)
				go func() {
					sent, err := w.generationProgress(context.Background(), w.generation, c, in, "我去核对一下刚才的结果～")
					result <- sent && err == nil
				}()
				ready := false
				deadline := time.Now().Add(3 * time.Second)
				for !ready && time.Now().Before(deadline) {
					w.drainGenerations(context.Background())
					select {
					case sent := <-result:
						if !sent {
							t.Fatal("model progress suppressed")
						}
						ready = true
					default:
						time.Sleep(time.Millisecond)
					}
				}
				if !ready {
					t.Fatal("progress blocked")
				}
			}
			if len(n.submissions) != 1 || n.submissions[0].Text != "我去核对一下刚才的结果～" {
				t.Fatal("not the model's actual progress", n.submissions)
			}
		})
	}
}
