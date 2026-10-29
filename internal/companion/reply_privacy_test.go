package companion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"wechat-bot/internal/mcptools"
)

type privacyTools struct {
	needed bool
	report string
	trace  Input
}

func (p *privacyTools) List(context.Context) ([]mcptools.Tool, error)                 { return nil, nil }
func (p *privacyTools) Call(context.Context, string, json.RawMessage) (string, error) { return "", nil }
func (p *privacyTools) NeedsPrivateReplyReview() bool                                 { return p.needed }
func (p *privacyTools) ForkTools() ToolAccess                                         { return p }
func (p *privacyTools) SetTrace(in Input)                                             { p.trace = in }
func (p *privacyTools) ReportPrivateDiagnostic(_ context.Context, s string) error {
	p.report = s
	return nil
}

type privacyModel struct {
	answer  string
	err     error
	calls   int
	answers []string
}

func (m *privacyModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	if len(m.answers) >= m.calls {
		return m.answers[m.calls-1], m.err
	}
	return m.answer, m.err
}

func TestSensitiveGroupUnsentRewriteUsesOriginalConversationForPrivateReport(t *testing.T) {
	tools := &privacyTools{}
	model := &privacyModel{answers: []string{`{"action":"replace","messages":["GPU 环境缺少模型"]}`, `{"messages":[],"private_report":"synthetic pending diagnosis"}`}}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(enabledConfig(), store, model, "test")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = tools
	in := Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "owner", IsGroup: true}
	d, err := e.ReviewPending(context.Background(), in, nil, []string{"old caption"}, nil)
	if err != nil || d.Action != "cancel" || tools.report == "" || tools.trace.Thread != in.Thread || tools.trace.Sender != in.Sender {
		t.Fatal("rewritten tail escaped review or used another trace", d, err, tools.trace)
	}
}
func TestSensitiveGroupDraftIsModelEditedAndMediaPreserved(t *testing.T) {
	tools := &privacyTools{needed: true}
	model := &privacyModel{answer: `{"messages":[],"private_report":"synthetic diagnosis"}`}
	e := &Engine{Tools: tools, model: model}
	d, err := e.reviewReplyPrivacy(context.Background(), Input{IsGroup: true}, Decision{Action: "reply", Messages: []string{"technical draft"}, MediaIDs: []string{"verified-media"}})
	if err != nil || tools.report != "synthetic diagnosis" || len(d.Messages) != 0 || d.Action != "reply" || d.MediaIDs[0] != "verified-media" {
		t.Fatal(d, err)
	}
	tools.needed = false
	_, _ = e.reviewReplyPrivacy(context.Background(), Input{IsGroup: true}, d)
	if model.calls != 1 {
		t.Fatal("reviewed ordinary chat")
	}
	if _, err = e.reviewReplyPrivacy(context.Background(), Input{IsGroup: true}, Decision{Action: "reply", Messages: []string{"GPU 环境故障"}}); err != nil || model.calls != 2 {
		t.Fatal("diagnostic draft bypassed review without tool call", err)
	}
	tools.needed = true
	model.err = errors.New("synthetic timeout")
	if _, err = e.reviewReplyPrivacy(context.Background(), Input{IsGroup: true}, Decision{Action: "reply", Messages: []string{"private draft"}}); err == nil {
		t.Fatal("published unreviewed private draft")
	}
}

func TestSensitiveGroupProgressReviewedBeforeToolExecution(t *testing.T) {
	tools := &privacyTools{}
	model := &privacyModel{answer: `{"messages":[],"private_report":"synthetic GPU diagnostic"}`}
	e := &Engine{Tools: tools, model: model}
	turns := []map[string]string{{"role": "user", "content": `{"kind":"current_message","text":"唱首歌","is_group":true}`}}
	text, err := e.reviewProgressPrivacy(context.Background(), turns, "server_exec", "synthetic environment draft")
	if err != nil || text != "" || tools.report == "" || model.calls != 1 {
		t.Fatal("progress escaped before tool marked diagnostics", text, err, tools.report)
	}
	turns[0]["content"] = `{"kind":"current_message","text":"check it","is_group":false}`
	text, err = e.reviewProgressPrivacy(context.Background(), turns, "server_exec", "private progress")
	if err != nil || text != "private progress" || model.calls != 1 {
		t.Fatal("changed private progress", text, err)
	}
}
