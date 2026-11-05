package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const hostStateHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestHostStateInitializeReadAndCAS(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	empty, err := s.ReadHostState(ctx, "mobile-control", "tasks")
	if err != nil || empty.Revision != 0 || len(empty.Records) != 0 {
		t.Fatalf("unexpected empty state: %#v err=%v", empty, err)
	}
	initial, err := s.InitializeHostState(ctx, "mobile-control", "tasks", hostStateHash, map[string]json.RawMessage{"task-1": json.RawMessage(`{"state":"queued"}`), "task-2": json.RawMessage(`null`)})
	if err != nil || initial.Revision != 1 {
		t.Fatalf("initialize: %#v err=%v", initial, err)
	}
	next, err := s.UpdateHostState(ctx, "mobile-control", "tasks", initial.Revision, map[string]json.RawMessage{"task-1": json.RawMessage(`{"state":"running"}`)}, []string{"task-2"})
	if err != nil || next != 2 {
		t.Fatalf("update revision=%d err=%v", next, err)
	}
	state, err := s.ReadHostState(ctx, "mobile-control", "tasks")
	if err != nil || state.Revision != 2 || string(state.Records["task-1"]) != `{"state":"running"}` || len(state.Records) != 1 || state.SourceHash != hostStateHash {
		t.Fatalf("read after update: %#v err=%v", state, err)
	}
	if _, err = s.UpdateHostState(ctx, "mobile-control", "tasks", 1, nil, nil); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale update err=%v", err)
	}
	if _, err = s.InitializeHostState(ctx, "mobile-control", "tasks", hostStateHash, nil); !errors.Is(err, ErrRevision) {
		t.Fatalf("duplicate initialize err=%v", err)
	}
}

func TestHostStateRejectsAtomicInvalidAndOversizeInputs(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.InitializeHostState(ctx, "mobile-control", "tasks", hostStateHash, map[string]json.RawMessage{"ok": json.RawMessage(`{"v":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateHostState(ctx, "mobile-control", "tasks", 1, map[string]json.RawMessage{"bad": json.RawMessage(`not-json`)}, nil); !errors.Is(err, ErrHostStateInput) {
		t.Fatalf("invalid update err=%v", err)
	}
	state, err := s.ReadHostState(ctx, "mobile-control", "tasks")
	if err != nil || state.Revision != 1 || len(state.Records) != 1 {
		t.Fatalf("failed update was not atomic: %#v err=%v", state, err)
	}
	tooLarge := json.RawMessage(`"` + strings.Repeat("a", maxHostStateRecordBytes) + `"`)
	if _, err = s.UpdateHostState(ctx, "mobile-control", "tasks", 1, map[string]json.RawMessage{"large": tooLarge}, nil); !errors.Is(err, ErrHostStateSize) {
		t.Fatalf("large record err=%v", err)
	}
	tooMany := make(map[string]json.RawMessage, 9)
	withinRecord := json.RawMessage(`"` + strings.Repeat("b", maxHostStateRecordBytes-2) + `"`)
	for index := 0; index < 9; index++ {
		tooMany["record-"+string(rune('a'+index))] = withinRecord
	}
	if _, err = s.InitializeHostState(ctx, "mobile-control", "overflow", hostStateHash, tooMany); !errors.Is(err, ErrHostStateSize) {
		t.Fatalf("total limit err=%v", err)
	}
	if state, readErr := s.ReadHostState(ctx, "mobile-control", "overflow"); readErr != nil || state.Revision != 0 {
		t.Fatalf("oversize initialize was not atomic: %#v err=%v", state, readErr)
	}
}

func TestHostStateRejectsIdentifiersAndRemainsAccountPrivate(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, input := range [][2]string{{"../mobile", "tasks"}, {"mobile-control", "../tasks"}, {"mobile-control", "tasks/other"}} {
		if _, err = s.ReadHostState(ctx, input[0], input[1]); !errors.Is(err, ErrHostStateInput) {
			t.Fatalf("identifier %q/%q err=%v", input[0], input[1], err)
		}
	}
	if _, err = s.InitializeHostState(ctx, "mobile-control", "tasks", hostStateHash, map[string]json.RawMessage{"private": json.RawMessage(`true`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Installed(ctx, "mobile-control"); err == nil {
		t.Fatal("host state unexpectedly installed a plugin")
	}
	if _, err = s.PrivateDirectory(ctx, "mobile-control"); err == nil {
		t.Fatal("host state unexpectedly exposed a plugin data directory")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(filepath.Clean(dir), "account-b"); err == nil {
		t.Fatal("cross-account store opened private host state")
	}
}

func TestHostStateWriteFailureDoesNotBecomeRevisionConflictOrLeaveState(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.db.Exec(`CREATE TRIGGER host_state_test_failure BEFORE INSERT ON host_state_records BEGIN SELECT RAISE(ABORT, 'synthetic host-state write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InitializeHostState(ctx, "mobile-control", "tasks", hostStateHash, map[string]json.RawMessage{"task": json.RawMessage(`true`)}); err == nil || errors.Is(err, ErrRevision) {
		t.Fatalf("write failure was misreported as revision conflict: %v", err)
	}
	state, err := s.ReadHostState(ctx, "mobile-control", "tasks")
	if err != nil || state.Revision != 0 || len(state.Records) != 0 {
		t.Fatalf("failed initialize left active state: %#v err=%v", state, err)
	}
}

func TestHostStateReadFailsClosedForCorruptStoredValues(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.InitializeHostState(ctx, "mobile-control", "tasks", hostStateHash, map[string]json.RawMessage{"task": json.RawMessage(`true`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE host_states SET source_hash='broken' WHERE plugin='mobile-control' AND namespace='tasks'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadHostState(ctx, "mobile-control", "tasks"); !errors.Is(err, ErrHostStateCorrupt) {
		t.Fatalf("corrupt source hash err=%v", err)
	}
	if _, err = s.db.Exec(`UPDATE host_states SET source_hash=? WHERE plugin='mobile-control' AND namespace='tasks'`, hostStateHash); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE host_state_records SET payload='not-json' WHERE plugin='mobile-control' AND namespace='tasks' AND key='task'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadHostState(ctx, "mobile-control", "tasks"); !errors.Is(err, ErrHostStateCorrupt) {
		t.Fatalf("corrupt record err=%v", err)
	}
}

func TestHostStateReadUsesOneSnapshotAcrossStores(t *testing.T) {
	dir := t.TempDir()
	reader, err := OpenStore(dir, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := OpenStore(dir, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	if _, err = reader.InitializeHostState(ctx, "mobile-control", "snapshot", hostStateHash, map[string]json.RawMessage{"pair": json.RawMessage(`{"revision":1}`)}); err != nil {
		t.Fatal(err)
	}
	var writerErr error
	var writerMu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		for revision := int64(2); revision <= 80; revision++ {
			state, readErr := writer.ReadHostState(ctx, "mobile-control", "snapshot")
			if readErr == nil {
				_, readErr = writer.UpdateHostState(ctx, "mobile-control", "snapshot", state.Revision, map[string]json.RawMessage{"pair": json.RawMessage(`{"revision":` + strconv.FormatInt(revision, 10) + `}`)}, nil)
			}
			if readErr != nil {
				writerMu.Lock()
				writerErr = readErr
				writerMu.Unlock()
				return
			}
		}
	}()
	for {
		select {
		case <-done:
			writerMu.Lock()
			err = writerErr
			writerMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
		state, readErr := reader.ReadHostState(ctx, "mobile-control", "snapshot")
		if readErr != nil {
			t.Fatal(readErr)
		}
		var payload struct {
			Revision int64 `json:"revision"`
		}
		if json.Unmarshal(state.Records["pair"], &payload) != nil || payload.Revision != state.Revision {
			t.Fatalf("mixed host-state snapshot: metadata=%d record=%s", state.Revision, state.Records["pair"])
		}
	}
}
