package companion

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type compressionScript struct {
	calls int
	chat  func(int) string
}

func (m *compressionScript) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	return m.chat(m.calls), nil
}

const repairedSummary = `{"version":1,"people":[],"topics":[],"current_focus":"合成摘要","media":[],"pending_tasks":[],"preferences":[],"corrections":[]}`

func TestCompressionRepairsSchemaAndRebasesActualContentChanges(t *testing.T) {
	for _, mode := range []string{"schema", "content", "display_name"} {
		t.Run(mode, func(t *testing.T) {
			cfg := enabledConfig()
			cfg.Permissions.Remember = true
			cfg.Context = &ContextSettings{true, 20, 5, 8000}
			store, _ := NewStore(t.TempDir())
			in := Input{Channel: "wechat", Account: "fixture", Thread: "room"}
			for i := 0; i < 25; i++ {
				in.TraceID, in.Text = fmt.Sprint(i), fmt.Sprint("original-", i)
				store.Observe(in, cfg, time.Now())
			}
			model := &compressionScript{chat: func(call int) string {
				if call == 1 {
					if mode == "schema" {
						return `{"version":1}`
					}
					store.transaction(func(data *persisted) error {
						s := data.Scopes[ScopeID(in.Channel, in.Account, in.Thread)]
						if mode == "content" {
							s.Messages[0].Text = "corrected observation"
						} else {
							s.Messages[0].SenderName = "resolved nickname"
						}
						data.Scopes[s.ID] = s
						return nil
					})
				}
				return repairedSummary
			}}
			engine, _ := New(cfg, store, model, "policy")
			if changed, err := engine.CompressContext(context.Background(), in, true); !changed || err != nil {
				t.Fatal("compression did not recover", err)
			}
			want := 2
			if mode == "display_name" {
				want = 1
			}
			if model.calls != want {
				t.Fatal("unexpected model work", model.calls, want)
			}
		})
	}
}
