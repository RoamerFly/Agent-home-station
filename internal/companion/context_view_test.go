package companion

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type parallelCompressionModel struct {
	started chan struct{}
	release chan struct{}
}

func (m *parallelCompressionModel) Chat(ctx context.Context, _ string, _ []map[string]string) (string, error) {
	m.started <- struct{}{}
	select {
	case <-m.release:
		return `{"version":1,"people":[],"topics":[],"current_focus":"合成摘要","media":[],"pending_tasks":[],"preferences":[],"corrections":[]}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestCompressionRunsIndependentlyPerScopeAndKeepsModelTail(t *testing.T) {
	cfg := enabledConfig()
	cfg.MemoryMessages = 500
	cfg.Permissions.Remember = true
	cfg.Context = &ContextSettings{true, 20, 5, 8000}
	store, _ := NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	inputs := []Input{{Channel: "wechat", Account: "fixture", Thread: "one"}, {Channel: "wechat", Account: "fixture", Thread: "two"}}
	for _, in := range inputs {
		for i := 0; i < 25; i++ {
			in.TraceID, in.Text = fmt.Sprint(i), fmt.Sprintf("synthetic-%d", i)
			if err := store.Observe(in, cfg, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	model := &parallelCompressionModel{make(chan struct{}, 2), make(chan struct{})}
	engine, _ := New(cfg, store, model, "policy")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { _, err := engine.CompressContext(ctx, inputs[0], true); results <- err }()
	<-model.started
	if _, err := engine.CompressContext(ctx, inputs[0], true); !errors.Is(err, ErrCompressionInProgress) {
		t.Fatal("same-scope overlap not detected", err)
	}
	go func() { _, err := engine.CompressContext(ctx, inputs[1], true); results <- err }()
	select {
	case <-model.started:
	case <-ctx.Done():
		t.Fatal("another conversation waited behind the first")
	}
	close(model.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range inputs {
		scope, _ := store.Read(ScopeID(in.Channel, in.Account, in.Thread), cfg, time.Now())
		if scope.CompressedCount != 20 || scope.FoldedHistoryCount() != 25 || len(store.Context(in, cfg, time.Now()).Messages) != 5 {
			t.Fatal("UI snapshot changed the model's recent context", scope.CompressedCount, scope.FoldedHistoryCount())
		}
		// An old-dated backlog turn appended later is new to the snapshot.
		in.Text, in.TraceID = "late observation", "late"
		store.Observe(in, cfg, time.Now().Add(-time.Hour))
		updated, _ := store.Read(scope.ID, cfg, time.Now())
		if updated.FoldedHistoryCount() != 25 || len(updated.Messages) != 26 {
			t.Fatal("late observation hidden by its source timestamp")
		}
	}
}

func TestPruningOldBacklogDoesNotMoveSummaryBoundary(t *testing.T) {
	now := time.Now()
	cfg := enabledConfig()
	cfg.MemoryMessages, cfg.MemoryDays = 500, 30
	scope := Scope{Summary: "summary", CompressedCount: 1, SummaryBoundary: 2, Messages: []Turn{{Time: now}, {Time: now}, {Time: now}, {Time: now.Add(-40 * 24 * time.Hour)}}}
	got := prune(scope, cfg, now)
	if len(got.Messages) != 3 || got.CompressedCount != 1 || got.FoldedHistoryCount() != 2 {
		t.Fatal("non-prefix expiry shifted covered messages", got.CompressedCount, got.FoldedHistoryCount())
	}
}

func TestLegacySummaryGetsReadOnlyPresentationBoundary(t *testing.T) {
	now := time.Now()
	scope := Scope{Summary: "legacy", CompressedCount: 1, SummaryUpdated: now, Messages: []Turn{{Time: now.Add(-time.Hour)}, {Time: now}, {Time: now.Add(time.Hour)}}}
	if scope.FoldedHistoryCount() != 2 || scope.SummaryBoundary != 0 {
		t.Fatal("legacy projection mutated source or included future messages")
	}
}
