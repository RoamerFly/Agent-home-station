package apphost

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type observationFixture struct {
	mu      sync.Mutex
	allowed bool
	version string
	pages   map[string]ObservationPage
	cursors []string
}

func (f *observationFixture) Connections(context.Context) ([]Connection, error) {
	return []Connection{{ID: "account", AccountID: "owner", Configuration: "ready"}}, nil
}
func (f *observationFixture) Inspect(context.Context, string) (ConnectionStatus, error) {
	return ConnectionStatus{}, nil
}
func (f *observationFixture) ObservationTargets(context.Context, string) ([]ObservationTarget, error) {
	return []ObservationTarget{{ContentID: "post", Label: "private title"}}, nil
}
func (f *observationFixture) ObservationGrant(context.Context, ObservationAccess) (ObservationGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ObservationGrant{Allowed: f.allowed, Version: f.version}, nil
}
func (f *observationFixture) ReadObservation(_ context.Context, r ObservationRequest) (ObservationPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors = append(f.cursors, r.Cursor)
	page, ok := f.pages[r.Cursor]
	if !ok {
		return ObservationPage{}, errors.New("missing page")
	}
	return page, nil
}

func observerFixtureRegistry(t *testing.T, f *observationFixture) Registry {
	t.Helper()
	r, err := New(Registration{Application: Application{ID: "offline", Name: "离线", InstallationID: "offline-test", Registration: "test"}, Provider: f})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestObserverResumesAndRechecksGrant(t *testing.T) {
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{
		"":     {Chunks: []ObservationChunk{{Position: 0, Text: "first"}}, Coverage: "body:page-1", NextCursor: "next"},
		"next": {Chunks: []ObservationChunk{{Position: 1, Text: "second"}}, Coverage: "body:complete", Complete: true},
	}}
	dir := t.TempDir()
	input := ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationBody, Limit: 1}
	first, err := NewObserver(observerFixtureRegistry(t, f), dir).Read(t.Context(), input)
	if err != nil || first.Complete || first.Resumed || len(first.Chunks) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := NewObserver(observerFixtureRegistry(t, f), dir).Read(t.Context(), input)
	if err != nil || !second.Complete || !second.Resumed || second.Chunks[0].Text != "second" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	f.mu.Lock()
	got := append([]string(nil), f.cursors...)
	f.allowed = false
	f.mu.Unlock()
	if len(got) != 2 || got[0] != "" || got[1] != "next" {
		t.Fatalf("cursors=%q", got)
	}
	if err := NewObserver(observerFixtureRegistry(t, f), dir).Check(t.Context(), second); !errors.Is(err, ErrObservationForbidden) {
		t.Fatalf("revoked check=%v", err)
	}
}

func TestObserverRejectsNoProgress(t *testing.T) {
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{"": {Chunks: []ObservationChunk{{Text: "partial"}}, Coverage: "body:partial"}}}
	_, err := NewObserver(observerFixtureRegistry(t, f), t.TempDir()).Read(t.Context(), ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationBody})
	if err == nil {
		t.Fatal("accepted a partial page without continuation")
	}
}
