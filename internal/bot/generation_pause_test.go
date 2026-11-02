package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func syntheticPartialEvidenceError() error {
	return fmt.Errorf("synthetic partial turn: %w", &companion.GenerationEvidenceChangedError{Stage: "after_tool_result", CompletedTools: []string{"synthetic_completed_tool"}, Continuation: "Read the saved original result; reassemble authorized sources before continuing. Do not replay the returned tool."})
}

func assertGenerationPaused(t *testing.T, w *Worker, records []Record, reports, sends int, returnedTool bool) {
	t.Helper()
	if reports != 0 || sends != 0 || len(w.runtime.Plans) != 0 || len(w.runtime.Wakes) != 0 {
		t.Fatal("partial turn generated a reply/retry/report", reports, sends, w.runtime.Plans)
	}
	found := false
	for _, r := range records {
		if r.Event == "model_failed" || r.Event == "tool_task_timeout" || r.Event == "tool_task_finished_without_result" {
			t.Fatal("partial success was relabeled as failed", r)
		}
		if r.Event != "generation_paused" {
			continue
		}
		var detail struct {
			Continuation   string   `json:"continuation"`
			CompletedTools []string `json:"completed_tools"`
		}
		if json.Unmarshal([]byte(r.DecisionNote), &detail) != nil || r.Status != "needs_review" || r.ModelStage == "" || detail.Continuation == "" {
			t.Fatal("continuation or completed tool identity lost", r)
		}
		if returnedTool && (r.ModelStage != "after_tool_result" || len(detail.CompletedTools) != 1 || detail.CompletedTools[0] != "synthetic_completed_tool") {
			t.Fatal("host returned-tool metadata lost", r)
		}
		if !returnedTool && len(detail.CompletedTools) != 0 {
			t.Fatal("provider fabricated completed tools", r)
		}
		found = true
	}
	if !found {
		t.Fatal("missing generation pause record")
	}
}

func TestTypedPartialGenerationRetainsContinuationWithoutFailureReply(t *testing.T) {
	w, c, n, in := planWorker(t, &planReviewModel{})
	records := []Record{}
	w.config.Record = func(r Record) { records = append(records, r) }
	reports := 0
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", in.TraceID, in.Thread, "incoming", "remote"), Phase: "tool_running"}}
	w.finishCharacter(context.Background(), c, in, companion.Decision{Action: "reply", Messages: []string{"stale result"}}, syntheticPartialEvidenceError(), context.DeadlineExceeded, true, true)
	assertGenerationPaused(t, w, records, reports, len(n.submissions), true)
	if w.runtime.Inbox[0].Phase != "needs_review" {
		t.Fatal("paused task was marked complete", w.runtime.Inbox[0].Phase)
	}
}

func TestPartialGenerationPropagatesPauseToTaskLifecycle(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "synchronous", true: "background"}[background], func(t *testing.T) {
			w, c, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
				return "", syntheticPartialEvidenceError()
			}))
			in := observeSyntheticMediaRequest(t, w, c, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "partial-task-run", TaskID: "partial-saved-task", Mode: "direct", Text: "合成原任务"})
			records := []Record{}
			w.config.Record = func(r Record) { records = append(records, r) }
			reports, finished := 0, ""
			w.config.AgentTaskDone = func(_ companion.Input, state string) { finished = state }
			if background {
				w.enableGenerations()
				defer w.closeGenerations()
				w.startGeneration(context.Background(), c, in, nil)
				pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
			} else {
				w.config.AgentTask = func(time.Time, func(string) bool) (*companion.Input, error) { return &in, nil }
				if !w.tickAgentTask(context.Background()) {
					t.Fatal("task did not run")
				}
			}
			assertGenerationPaused(t, w, records, reports, len(n.submissions), false)
			if finished != "generation_paused" {
				t.Fatal("task lifecycle lost the paused state", finished)
			}
		})
	}
}

func TestRetryableModelFailurePersistsInboxWithoutReplayingTools(t *testing.T) {
	w, _, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
	in := companion.Input{Thread: "filehelper", TraceID: "retry-model", Sender: "person", Mode: "direct"}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("retry-model-event", in.TraceID, in.Thread, "incoming", "remote"), Phase: "model_running"}}
	err := &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: "model_rate_limited", Retryable: true, RetryDelayMS: int64((2 * time.Minute).Milliseconds())}}
	before := time.Now()
	if !w.scheduleModelContinuation(in, err, false) {
		t.Fatal("retryable model failure was not persisted")
	}
	item := w.runtime.Inbox[0]
	if item.Phase != "retrying_model" || item.RetryAfter.Before(before.Add(119*time.Second)) {
		t.Fatal("retry checkpoint lost provider delay", item)
	}
	item.ToolStarted = true
	item.Phase = "model_running"
	if w.scheduleModelContinuation(in, err, true) || item.Phase != "model_running" {
		t.Fatal("tool-started generation was blindly replayed", item)
	}
	for _, code := range []string{"model_authentication_failed", "model_insufficient_balance"} {
		if w.scheduleModelContinuation(in, &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: code, Retryable: false}}, false) {
			t.Fatal("terminal provider error scheduled a retry", code)
		}
	}
}

func TestTaskModelFailureUsesOnePersistentRetryCallback(t *testing.T) {
	w, _, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
	in := companion.Input{Thread: "filehelper", TraceID: "task-model-retry", Sender: "person", TaskID: "saved-task", Mode: "direct"}
	var gotInput companion.Input
	var gotDelay time.Duration
	var gotReason string
	w.config.AgentTaskRetry = func(received companion.Input, delay time.Duration, reason string) {
		gotInput, gotDelay, gotReason = received, delay, reason
	}
	err := &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: "model_service_unavailable", Retryable: true}}
	if !w.scheduleModelContinuation(in, err, false) || gotInput.TaskID != in.TaskID || gotDelay < 2*time.Minute || gotReason != "model_service_unavailable" {
		t.Fatal("task retry callback did not retain bounded recovery state", gotInput, gotDelay, gotReason)
	}
}

func TestReadOnlyToolCheckpointIsPersistedAndAllowsModelOnlyContinuation(t *testing.T) {
	w, _, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return `{"action":"silent"}`, nil }))
	in := companion.Input{Thread: "filehelper", TraceID: "checkpointed-retry", Sender: "person", Mode: "direct"}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("checkpointed-retry-event", in.TraceID, in.Thread, "incoming", "remote"), Phase: "model_running"}}
	j := &generationJob{input: in}
	args := json.RawMessage(`{"scope":"current"}`)
	stamp := companion.GenerationEvidence{RecallEpoch: "synthetic-epoch"}
	if err := w.appendGenerationCheckpoint(j, "memory_search", args, `{"items":["已读资料"]}`, "retained-result", stamp, true); err != nil {
		t.Fatal(err)
	}
	w.runtime.Inbox[0].Phase = "retrying_model"
	restored := w.restoreGenerationCheckpoint(in)
	if len(restored.RecoveryToolResults) != 1 || !restored.RecoveryToolResults[0].ReadOnly || restored.RecoveryToolResults[0].CallKey != generationToolCallKey("memory_search", args) || restored.RecoveryEvidence == nil || *restored.RecoveryEvidence != stamp {
		t.Fatal("read-only checkpoint not retained with its evidence", restored)
	}
	err := &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: "model_service_unavailable", Retryable: true}}
	if !w.scheduleModelContinuation(restored, err, true) || w.runtime.Inbox[0].Phase != "retrying_model" {
		t.Fatal("model-only continuation discarded completed read-only checkpoint", w.runtime.Inbox[0])
	}
}

func TestGenerationToolCheckpointKeyCanonicalizesJSONArguments(t *testing.T) {
	first := generationToolCallKey("video_inspect", json.RawMessage(`{"message_id":"m","action":"info"}`))
	second := generationToolCallKey("video_inspect", json.RawMessage("{ \n \t\"action\": \"info\", \"message_id\": \"m\" }"))
	if first != second {
		t.Fatal("equivalent JSON could replay a completed tool call", first, second)
	}
}

func TestCrossConversationRecoveryCheckpointFailsClosedBeforeModelInput(t *testing.T) {
	calls := 0
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		calls++
		return `{"action":"reply","messages":["must not see checkpoint"]}`, nil
	}))
	trace := "cross-recovery"
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("cross-recovery-event", trace, "room@chatroom", "incoming", "remote"), Phase: "retrying_model", RecoveryTools: []companion.RecoveryToolResult{{Tool: "wechat_read", CallKey: "cross-read", Result: `{"private":"raw other conversation"}`, ReadOnly: true}}, RecoveryPrivacy: &companion.RecoveryPrivacyBinding{Required: true, Target: "owner", SourceThreads: []string{"private-thread"}}}}
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Account: "test", Thread: "room@chatroom", Sender: "owner", TraceID: trace, IsGroup: true, Mode: "direct", Text: "继续"}
	w.startGeneration(context.Background(), c, in, nil)
	if calls != 0 || w.runtime.Inbox[0].Phase != "needs_review" {
		t.Fatal("cross-conversation checkpoint reached model after route reset", calls, w.runtime.Inbox[0])
	}
}

func TestDirectCrossConversationRecoveryCheckpointAlsoFailsClosed(t *testing.T) {
	calls := 0
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		calls++
		return `{"action":"reply","messages":["must not see checkpoint"]}`, nil
	}))
	in := companion.Input{Account: "test", Thread: "room@chatroom", Sender: "owner", TraceID: "direct-cross-recovery", IsGroup: true, Mode: "direct", Text: "继续", RecoveryToolResults: []companion.RecoveryToolResult{{Tool: "wechat_read", Result: `{"private":"raw"}`, ReadOnly: true}}, RecoveryPrivacy: &companion.RecoveryPrivacyBinding{Required: true}, RecoveryBlocked: true}
	if outcome := w.respondCharacter(context.Background(), c, in); outcome != "generation_paused" || calls != 0 {
		t.Fatal("direct recovery exposed cross result", outcome, calls)
	}
}
