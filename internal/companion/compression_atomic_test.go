package companion

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

type compressionFailureModel struct {
	calls int
	err   error
	out   string
}

func (m *compressionFailureModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	if m.err != nil {
		return "", m.err
	}
	return m.out, nil
}

func compressionFixture(t *testing.T) (Config, *Store, Input) {
	t.Helper()
	cfg := enabledConfig()
	cfg.MemoryMessages = 500
	cfg.Permissions.Remember = true
	cfg.Context = &ContextSettings{true, 20, 5, 8000}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	in := Input{Channel: "wechat", Account: "synthetic", Thread: "room", Sender: "owner"}
	for i := 0; i < 25; i++ {
		in.TraceID, in.Text = fmt.Sprint(i), fmt.Sprintf("原始消息-%02d", i)
		if err := store.Observe(in, cfg, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, store, in
}

func TestCompressionHTTP200TruncationFallsBackAndWritesAtomically(t *testing.T) {
	cfg, store, in := compressionFixture(t)
	model := &compressionFailureModel{err: &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: "model_output_token_limit", HTTPStatus: 200, Attempts: 2}}}
	engine, err := New(cfg, store, model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := engine.CompressContext(context.Background(), in, true)
	if err != nil || !changed {
		t.Fatalf("HTTP 200 truncation was not recovered: changed=%v err=%v", changed, err)
	}
	scope, err := store.Read(ScopeID(in.Channel, in.Account, in.Thread), cfg, time.Now())
	if err != nil || scope.Summary == "" || scope.CompressedCount != 20 || len(scope.Messages) != 25 {
		t.Fatalf("atomic fallback write missing: err=%v summary=%q compressed=%d messages=%d", err, scope.Summary, scope.CompressedCount, len(scope.Messages))
	}
	if scope.Messages[0].Text != "原始消息-00" {
		t.Fatal("fallback changed the retained original message")
	}
}

func TestCompressionValidationFailureKeepsOriginalSummaryAndMessages(t *testing.T) {
	cfg, store, in := compressionFixture(t)
	id := ScopeID(in.Channel, in.Account, in.Thread)
	if err := store.scopeTransaction(id, func(data *persisted) error {
		scope := data.Scopes[id]
		scope.Summary = `{"version":1,"current_focus":"原始摘要","people":[],"topics":[],"media":[],"pending_tasks":[],"preferences":[],"corrections":[]}`
		data.Scopes[id] = scope
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	model := &compressionFailureModel{out: `{"version":1}`}
	engine, err := New(cfg, store, model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := engine.CompressContext(context.Background(), in, true)
	if changed || err == nil {
		t.Fatalf("invalid summary was reported as committed: changed=%v err=%v", changed, err)
	}
	scope, readErr := store.Read(id, cfg, time.Now())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if scope.Summary == "" || scope.CompressedCount != 0 || len(scope.Messages) != 25 || scope.Messages[0].Text != "原始消息-00" {
		t.Fatal("failed compression did not preserve original memory", scope.Summary, scope.CompressedCount, len(scope.Messages))
	}
}

func TestCompressionFailureReleasesInProgressStateForRetry(t *testing.T) {
	cfg, store, in := compressionFixture(t)
	model := &compressionFailureModel{err: errors.New("synthetic provider failure")}
	engine, err := New(cfg, store, model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := engine.CompressContext(context.Background(), in, true); changed || err == nil {
		t.Fatalf("first compression unexpectedly succeeded: changed=%v err=%v", changed, err)
	}
	model.err = nil
	model.out = repairedSummary
	if changed, err := engine.CompressContext(context.Background(), in, true); !changed || err != nil {
		t.Fatalf("retry remained blocked after failure: changed=%v err=%v", changed, err)
	}
}
