package mcptools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type bridgeFixtureProcess struct {
	input        *io.PipeWriter
	serverInput  *io.PipeReader
	output       *io.PipeReader
	serverOutput *io.PipeWriter
	closed       chan struct{}
	done         chan struct{}
	once         sync.Once
}

func newBridgeFixtureProcess(handle func(string, json.RawMessage, <-chan struct{}) json.RawMessage) *bridgeFixtureProcess {
	in, input := io.Pipe()
	output, out := io.Pipe()
	p := &bridgeFixtureProcess{input: input, serverInput: in, output: output, serverOutput: out, closed: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), 2*maxFrame)
		for scanner.Scan() {
			var frame struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				continue
			}
			if frame.ID == 0 {
				continue
			}
			result := json.RawMessage(`{"protocolVersion":"2025-06-18"}`)
			if frame.Method != "initialize" {
				result = handle(frame.Method, append(json.RawMessage(nil), frame.Params...), p.closed)
			}
			select {
			case <-p.closed:
				return
			default:
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": result})
			if _, err := out.Write(append(raw, '\n')); err != nil {
				return
			}
		}
	}()
	return p
}
func (p *bridgeFixtureProcess) Stdin() io.WriteCloser { return p.input }
func (p *bridgeFixtureProcess) Stdout() io.ReadCloser { return p.output }
func (p *bridgeFixtureProcess) Wait() error           { <-p.done; return nil }
func (p *bridgeFixtureProcess) Close() error {
	p.once.Do(func() {
		close(p.closed)
		_ = p.input.Close()
		_ = p.serverInput.Close()
		_ = p.output.Close()
		_ = p.serverOutput.Close()
	})
	select {
	case <-p.done:
		return nil
	case <-time.After(time.Second):
		return errors.New("synthetic bridge close unconfirmed")
	}
}

func bridgeFixtureHub(t *testing.T, handle func(string, json.RawMessage, <-chan struct{}) json.RawMessage, options ...Option) *Hub {
	t.Helper()
	options = append(options, WithStdioLauncher(func(context.Context, ServerConfig) (StdioProcess, error) { return newBridgeFixtureProcess(handle), nil }))
	hub, err := New([]ServerConfig{{ID: "fixture", PluginID: "synthetic", Transport: "stdio", Enabled: true, Tools: []string{"echo"}}}, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := hub.Close(); err != nil {
			t.Error(err)
		}
	})
	return hub
}

func TestBridgeRequestPreservesLargeResultAllMediaAndNumberLexemes(t *testing.T) {
	content := []map[string]string{{"type": "text", "text": strings.Repeat("full text ", 4000)}}
	for range 4 {
		content = append(content, map[string]string{"type": "image", "mimeType": "image/png", "data": "synthetic"})
	}
	expected, _ := json.Marshal(map[string]any{"content": content, "isError": false})
	params := json.RawMessage(`{"name":"echo","arguments":{"integer":9007199254740993123456789,"decimal":-0.0001200,"exponent":1.234567890123456789e+42}}`)
	var observedMu sync.Mutex
	var observed []json.RawMessage
	hub := bridgeFixtureHub(t, func(method string, raw json.RawMessage, _ <-chan struct{}) json.RawMessage {
		observedMu.Lock()
		observed = append(observed, raw)
		observedMu.Unlock()
		return expected
	})
	for _, ctx := range []context.Context{t.Context(), WithSessionScope(t.Context(), "scope")} {
		result, err := hub.Request(ctx, "fixture", "tools/call", params)
		if err != nil || !bytes.Equal(result, expected) {
			t.Fatal("raw result truncated or changed", len(result), len(expected), err)
		}
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	if len(observed) != 2 {
		t.Fatal("wrong call count", len(observed))
	}
	for _, raw := range observed {
		if !bytes.Equal(raw, params) {
			t.Fatal("number lexemes changed", string(raw))
		}
	}
}

func TestBridgeRequestRejectsInvalidScopeMethodsParamsAndDisallowedTools(t *testing.T) {
	var attempts atomic.Int32
	hub := bridgeFixtureHub(t, func(string, json.RawMessage, <-chan struct{}) json.RawMessage {
		attempts.Add(1)
		return json.RawMessage(`{}`)
	})
	invalid := []struct{ server, method, params string }{
		{"missing", "tools/list", `{}`}, {"fixture", "initialize", `{}`}, {"fixture", "resources/read", `{}`},
		{"fixture", "tools/list", `null`}, {"fixture", "tools/list", `[]`}, {"fixture", "tools/list", `{"cursor":1}`},
		{"fixture", "tools/list", `{"cursor":"a","cursor":"b"}`}, {"fixture", "tools/list", `{"cursor":"a","unknown":1}`},
		{"fixture", "tools/list", `{} {}`}, {"fixture", "tools/list", `{"unknown":1}`},
		{"fixture", "tools/call", `{"name":"hidden","arguments":{}}`}, {"fixture", "tools/call", `{"name":"echo","arguments":[]}`},
		{"fixture", "tools/call", `{"name":"echo","arguments":null}`}, {"fixture", "tools/call", `{"name":"echo","unknown":{}}`},
		{"fixture", "tools/call", `{"name":"hidden","name":"echo"}`}, {"fixture", "tools/call", `{"name":"echo","arguments":{"a":1,"a":2}}`},
		{"fixture", "tools/call", `{"name":"echo","arguments":{"nested":[{"a":1,"a":2}]}}`},
	}
	for _, item := range invalid {
		if _, err := hub.Request(t.Context(), item.server, item.method, json.RawMessage(item.params)); err == nil {
			t.Fatal("invalid raw request admitted", item)
		}
	}
	if attempts.Load() != 0 {
		t.Fatal("invalid request reached transport", attempts.Load())
	}
	disabled, err := New([]ServerConfig{{ID: "disabled", PluginID: "synthetic", Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	if _, err = disabled.Request(t.Context(), "disabled", "tools/call", json.RawMessage(`{"name":"echo"}`)); err == nil {
		t.Fatal("disabled server selected")
	}
	unregistered, err := New([]ServerConfig{{ID: "unregistered", PluginID: "synthetic", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer unregistered.Close()
	for _, ctx := range []context.Context{t.Context(), WithSessionScope(t.Context(), "scope")} {
		if _, err = unregistered.Request(ctx, "unregistered", "tools/list", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "sandbox_required") {
			t.Fatal("raw request bypassed required stdio launcher", err)
		}
	}
}

func TestBridgeRequestCursorUsesExactNextPageAndGuardRevokesCachedDiscovery(t *testing.T) {
	first := json.RawMessage(`{"tools":[{"name":"echo"},{"name":"not-allowed-to-call"}],"nextCursor":"next / 中文"}`)
	second := json.RawMessage(`{"tools":[{"name":"second-page"}]}`)
	var pages atomic.Int32
	var revoked atomic.Bool
	hub := bridgeFixtureHub(t, func(method string, raw json.RawMessage, _ <-chan struct{}) json.RawMessage {
		pages.Add(1)
		var params map[string]string
		if json.Unmarshal(raw, &params) != nil {
			t.Error("cursor params not an object")
		}
		if params["cursor"] == "next / 中文" {
			return second
		}
		return first
	}, WithServerGuard(func(context.Context, ServerConfig) error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}))
	ctx := WithSessionScope(t.Context(), "scope")
	for _, item := range []struct {
		params string
		want   json.RawMessage
	}{{`{}`, first}, {`{"cursor":"next / 中文"}`, second}, {`{}`, first}} {
		result, err := hub.Request(ctx, "fixture", "tools/list", json.RawMessage(item.params))
		if err != nil || !bytes.Equal(result, item.want) {
			t.Fatal("wrong catalogue page", string(result), err)
		}
	}
	if pages.Load() != 2 {
		t.Fatal("cursor reused first-page cache or fetched extra pages", pages.Load())
	}
	revoked.Store(true)
	if _, err := hub.Request(ctx, "fixture", "tools/list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("cached raw page retained revoked authority")
	}
	if pages.Load() != 2 || len(hub.sessions) != 0 {
		t.Fatal("revoked raw request called or retained session", pages.Load(), len(hub.sessions))
	}
}

func TestBridgeRequestCancelledUnknownCallDoesNotReplay(t *testing.T) {
	var attempts atomic.Int32
	entered := make(chan struct{}, 1)
	hub := bridgeFixtureHub(t, func(method string, _ json.RawMessage, closed <-chan struct{}) json.RawMessage {
		if method == "tools/call" {
			attempts.Add(1)
			entered <- struct{}{}
			<-closed
		}
		return json.RawMessage(`{"tools":[]}`)
	})
	base := WithSessionScope(t.Context(), "scope")
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := hub.Request(ctx, "fixture", "tools/call", json.RawMessage(`{"name":"echo","arguments":{}}`))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fixture call not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not close raw transport")
	}
	if _, err := hub.Request(base, "fixture", "tools/list", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 1 {
		t.Fatal("unknown call replayed", attempts.Load())
	}
}

func TestBridgeRequestHTTPKeepsRawResultsAndTransportBoundary(t *testing.T) {
	var large atomic.Bool
	params := json.RawMessage(`{"name":"echo","arguments":{"n":9007199254740993123456789,"e":1.234567890123e+90}}`)
	expected := json.RawMessage(`{"content":[{"type":"text","text":"raw"}],"value":9007199254740993123456789}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var frame struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&frame) != nil {
			t.Error("invalid request")
			return
		}
		if frame.ID == 0 {
			w.WriteHeader(202)
			return
		}
		result := json.RawMessage(`{}`)
		if frame.Method == "tools/call" {
			if !bytes.Equal(frame.Params, params) {
				t.Error("HTTP number lexemes changed", string(frame.Params))
			}
			result = expected
			if large.Load() {
				raw, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", maxFrame+1)})
				result = raw
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": result})
	}))
	defer server.Close()
	hub, err := New([]ServerConfig{{ID: "http", Transport: "http", Enabled: true, URL: server.URL, Tools: []string{"echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	result, err := hub.Request(t.Context(), "http", "tools/call", params)
	if err != nil || !bytes.Equal(result, expected) {
		t.Fatal("HTTP raw result changed", string(result), err)
	}
	large.Store(true)
	if result, err = hub.Request(t.Context(), "http", "tools/call", params); err == nil || len(result) != 0 || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatal("HTTP transport boundary hidden", len(result), err)
	}
}

func TestBridgeRequestStdioOversizedResultFailsExplicitlyWithoutPartialJSON(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", maxFrame+1)})
	hub := bridgeFixtureHub(t, func(string, json.RawMessage, <-chan struct{}) json.RawMessage { return raw })
	result, err := hub.Request(WithSessionScope(t.Context(), "scope"), "fixture", "tools/call", json.RawMessage(`{"name":"echo"}`))
	if err == nil || len(result) != 0 || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatal("stdio transport boundary hidden", len(result), err)
	}
}
