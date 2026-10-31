package companion

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/mcptools"
)

type nativeMusicProgressModel struct{ step int }

func (*nativeMusicProgressModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("must use native functions")
}
func (m *nativeMusicProgressModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	m.step++
	if m.step == 1 {
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"music_local_start"}}`, nil
	}
	if m.step == 2 {
		return `{"action":"tool","tool":"music_local_start","progress":"我开始做这首忧郁风的小曲啦～","arguments":{"style":"folk","lyrics":"[verse]雨过天晴"}}`, nil
	}
	return `{"action":"reply","messages":["正在做啦" ]}`, nil
}

type nativeMusicProgressTools struct{ before func() }

func (*nativeMusicProgressTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "music_local_start", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (t *nativeMusicProgressTools) Call(context.Context, string, json.RawMessage) (string, error) {
	t.before()
	return `{"job_id":"one","automatic_followup":true}`, nil
}
func TestNativeMusicHasAcknowledgementBeforeGenerationAfterToolDiscovery(t *testing.T) {
	for _, task := range []bool{false, true} {
		cfg := enabledConfig()
		store, _ := NewStore(t.TempDir())
		if opened := store; opened != nil {
			t.Cleanup(func() { _ = opened.Close() })
		}
		engine, _ := New(cfg, store, &nativeMusicProgressModel{}, "global")
		acks := 0
		calls := 0
		engine.Tools = &nativeMusicProgressTools{before: func() {
			calls++
			want := 1
			if acks != want {
				t.Fatal("acknowledgement not ordered before actual work", acks, want)
			}
		}}
		engine.MaxToolCalls = 4
		in := Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", Mode: "direct", TraceID: "one", Text: "可以，忧郁风格吧"}
		in = syntheticNativeInput(in, "one")
		if task {
			in.TaskID = "continuation"
		}
		ctx := WithProgress(context.Background(), func(_ context.Context, text string) (bool, error) {
			if text != "" {
				if text != "我开始做这首忧郁风的小曲啦～" {
					t.Fatal("program replaced model-authored progress", text)
				}
				acks++
				return true, nil
			}
			return false, nil
		})
		d, err := engine.Respond(ctx, in)
		if err != nil || d.Action != "reply" || calls != 1 || acks != 1 {
			t.Fatal("ack missing or repeated", err, d, calls, acks)
		}
	}
}
