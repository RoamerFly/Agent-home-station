package bot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type diagnosticModel struct{ calls int }

func (model *diagnosticModel) Chat(context.Context, string, []map[string]string) (string, error) {
	model.calls++
	if model.calls == 1 {
		return `{"action":"tool","tool":"synthetic_tool","arguments":{}}`, nil
	}
	return `{"action":"silent"}`, nil
}

type diagnosticTools struct{}

func (diagnosticTools) ForkTools() companion.ToolAccess { return diagnosticTools{} }

func (diagnosticTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "synthetic_tool", InputSchema: json.RawMessage(`{}`)}}, nil
}
func (diagnosticTools) Call(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
	return "synthetic result", ctx.Err()
}

func TestBotToolDiagnosticsReachSynchronousAndGenerationRecords(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "synchronous", true: "generation"}[background], func(t *testing.T) {
			model := &diagnosticModel{}
			worker, engine, _ := backgroundWorker(t, model)
			engine.Tools = diagnosticTools{}
			preview, err := companion.NewPreview(engine.Config, model, "synthetic role")
			if err != nil {
				t.Fatal(err)
			}
			preview.Tools = diagnosticTools{}
			preview.MaxToolCalls = 2
			records := []Record{}
			worker.config.Record = func(record Record) {
				if record.Tool != "" {
					records = append(records, record)
				}
			}
			input := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "synthetic-tool-turn", Text: "执行合成工具", Mode: "direct"}
			input.Preview = true
			if background {
				worker.enableGenerations()
				defer worker.closeGenerations()
				// Install a generation context without an active job: this exercises
				// the exact owner-loop observation bridge while avoiding delivery.
				ctx := worker.generationContext(context.Background(), input)
				done := make(chan struct{})
				go func() { defer close(done); _, _ = preview.Respond(ctx, input) }()
				deadline := time.Now().Add(5 * time.Second)
				finished := false
				for !finished && time.Now().Before(deadline) {
					worker.drainGenerations(context.Background())
					select {
					case <-done:
						finished = true
					default:
						time.Sleep(time.Millisecond)
					}
				}
				if !finished {
					t.Fatal("generation observation blocked")
				}
			} else {
				_, err := preview.Respond(worker.modelContext(context.Background(), input.Thread, input.TraceID), input)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(records) != 2 || records[0].Event != "tool_started" || records[1].Event != "tool_completed" || records[0].CallID == "" || records[0].CallID != records[1].CallID || records[1].ToolAttempt != 1 || records[1].MessageID != input.TraceID {
				t.Fatal("tool records lost", records)
			}
		})
	}
}

type cancellingDiagnosticTools struct {
	diagnosticTools
	cancel context.CancelFunc
}

func (tools cancellingDiagnosticTools) Call(context.Context, string, json.RawMessage) (string, error) {
	tools.cancel()
	return "", context.Canceled
}

func TestBotToolDiagnosticsCancellationUsesCurrentGeneration(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "replaced"}[replaced], func(t *testing.T) {
			model := &diagnosticModel{}
			worker, engine, _ := backgroundWorker(t, model)
			engine.Tools = diagnosticTools{}
			worker.enableGenerations()
			defer worker.closeGenerations()
			input := companion.Input{Thread: "filehelper", TraceID: "synthetic-cancelled-call", Text: "执行合成工具", Mode: "direct", Preview: true}
			job := &generationJob{input: input, cancel: func() {}}
			worker.generation.jobs[input.Thread] = job
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = worker.generationContext(ctx, input)
			if replaced {
				worker.generation.jobs[input.Thread] = &generationJob{cancel: func() {}}
			}
			preview, err := companion.NewPreview(engine.Config, model, "synthetic role")
			if err != nil {
				t.Fatal(err)
			}
			preview.Tools, preview.MaxToolCalls = cancellingDiagnosticTools{cancel: cancel}, 2
			records := []Record{}
			worker.config.Record = func(record Record) {
				if record.Tool != "" {
					records = append(records, record)
				}
			}
			done := make(chan struct{})
			go func() { defer close(done); _, _ = preview.Respond(ctx, input) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				worker.drainGenerations(context.Background())
				select {
				case <-done:
					if replaced {
						if len(records) != 0 {
							t.Fatal("replaced generation appended stale tool records", records)
						}
					} else if len(records) != 2 || records[0].Event != "tool_started" || records[1].Event != "tool_cancelled" || records[1].Status != "cancelled" || records[0].CallID != records[1].CallID {
						t.Fatal("cancelled tool lost its terminal record", records)
					}
					return
				default:
					if time.Now().After(deadline) {
						t.Fatal("generation cancellation observation blocked")
					}
					time.Sleep(time.Millisecond)
				}
			}
		})
	}
}
