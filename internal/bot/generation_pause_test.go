package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
	"wechat-bot/internal/companion"
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
	w.config.ReportFailure = func(companion.Input, string) error { reports++; return nil }
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
			w.config.ReportFailure = func(companion.Input, string) error { reports++; return nil }
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
