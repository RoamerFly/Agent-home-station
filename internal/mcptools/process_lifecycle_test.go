package mcptools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type closeBlockingWriter struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (w *closeBlockingWriter) Write([]byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.closed
	return 0, io.ErrClosedPipe
}
func (w *closeBlockingWriter) Close() error { w.once.Do(func() { close(w.closed) }); return nil }

type retryProcess struct {
	mu    sync.Mutex
	calls int
}

func (p *retryProcess) terminate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return errors.New("synthetic native cleanup uncertain")
	}
	return nil
}

func (p *retryProcess) Calls() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

func TestSessionCloseRetriesUnconfirmedNativeTermination(t *testing.T) {
	p := &retryProcess{}
	s := &mcpSession{process: p}
	if err := s.close(); err == nil {
		t.Fatal("first native cleanup failure was hidden")
	}
	if !s.dead.Load() || p.Calls() != 1 {
		t.Fatalf("first close state = dead:%v calls:%d", s.dead.Load(), p.Calls())
	}
	if err := s.close(); err != nil || p.Calls() != 2 || s.process != nil {
		t.Fatalf("retry close = %v calls:%d process:%#v", err, p.Calls(), s.process)
	}
}

func TestHubCloseRetainsUnconfirmedSessionForRetry(t *testing.T) {
	hub, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &retryProcess{}
	hub.sessions["failed"] = &mcpSession{process: p}
	if err = hub.Close(); err == nil || len(hub.sessions) != 1 {
		t.Fatalf("first Hub.Close = %v sessions=%d", err, len(hub.sessions))
	}
	if err = hub.Close(); err != nil || len(hub.sessions) != 0 || p.Calls() != 2 {
		t.Fatalf("retry Hub.Close = %v sessions=%d calls=%d", err, len(hub.sessions), p.Calls())
	}
}

func TestRPCDoesNotDereferenceTransportClearedByClose(t *testing.T) {
	s := &mcpSession{}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rpc(context.Background(), "initialize", initParams(), true); err == nil {
		t.Fatal("RPC used a transport after Close")
	}
}

func TestStartThenCloseBeforeInitializeDoesNotPanic(t *testing.T) {
	s := &mcpSession{cfg: ServerConfig{
		Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestPersistentMCPChild$"},
		Env: map[string]string{"WECHAT_MCP_SESSION_CHILD": "1"},
	}}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rpc(context.Background(), "initialize", initParams(), true); err == nil {
		t.Fatal("initialization wrote after startup cancellation")
	}
}

func TestRPCTransportSnapshotSurvivesConcurrentClose(t *testing.T) {
	writer := &closeBlockingWriter{entered: make(chan struct{}, 1), closed: make(chan struct{})}
	s := &mcpSession{input: writer, reader: bufio.NewReader(strings.NewReader(""))}
	done := make(chan error, 1)
	go func() {
		_, err := s.rpc(context.Background(), "initialize", initParams(), true)
		done <- err
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("RPC did not enter synthetic transport")
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("RPC result after concurrent close = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt the snapshotted transport")
	}
}

// TestMCPProcessTreeFixture is a self-contained stdio MCP server used by the
// lifecycle test below. Its tool spawns a delayed marker child, then blocks so
// the host cancellation has to terminate a live process tree. No Python, ADB,
// network or external provider is involved.
func TestMCPProcessTreeFixture(t *testing.T) {
	if os.Getenv("WECHAT_MCP_TREE_CHILD") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request rpcRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		switch request.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{}}}`+"\n", request.ID)
		case "notifications/initialized":
		case "tools/list":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"spawn","inputSchema":{"type":"object"}}]}}`+"\n", request.ID)
		case "tools/call":
			// cmd.exe plus timeout is a local synthetic grandchild; if closing
			// kills only this server parent, it writes the marker a second later.
			marker := os.Getenv("WECHAT_MCP_TREE_MARKER")
			child := exec.Command("cmd.exe", "/d", "/s", "/c", `timeout /t 1 /nobreak >NUL & echo escaped > "`+marker+`"`)
			if err := child.Start(); err != nil {
				os.Exit(3)
			}
			_ = os.WriteFile(os.Getenv("WECHAT_MCP_TREE_READY"), []byte("ready"), 0600)
			time.Sleep(time.Hour)
		}
	}
}

func waitForLifecycleFile(ctx context.Context, path string) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func TestPersistentStdioCancellationTerminatesChildTree(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows Job Object lifecycle test")
	}
	dir := t.TempDir()
	ready := filepath.Join(dir, "child-ready")
	marker := filepath.Join(dir, "child-marker")
	hub, err := New([]ServerConfig{{
		ID: "tree", Enabled: true, Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestMCPProcessTreeFixture$"},
		Env:  map[string]string{"WECHAT_MCP_TREE_CHILD": "1", "WECHAT_MCP_TREE_READY": ready, "WECHAT_MCP_TREE_MARKER": marker},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	base := WithSessionScope(context.Background(), "tree-lifecycle")
	if _, err = hub.List(base); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(base)
	callDone := make(chan error, 1)
	go func() {
		_, callErr := hub.Call(ctx, "tree__spawn", json.RawMessage(`{}`))
		callDone <- callErr
	}()
	waitCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err = waitForLifecycleFile(waitCtx, ready); err != nil {
		cancel()
		t.Fatalf("grandchild readiness: %v", err)
	}
	cancel()
	select {
	case err = <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled persistent call did not return")
	}
	// Give the fixture marker enough time to expose an escaped grandchild.
	time.Sleep(700 * time.Millisecond)
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived session cancellation: %v", err)
	}
	if err = hub.Close(); err != nil {
		t.Fatal(err)
	}
	if err = hub.Close(); err != nil {
		t.Fatal(err)
	}
}
