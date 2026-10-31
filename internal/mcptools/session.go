package mcptools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type sessionScopeKey struct{}

// WithSessionScope isolates server state between conversations. Omitting it
// retains the short-lived request behavior for manual tests and previews.
func WithSessionScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, sessionScopeKey{}, scope)
}

type mcpSession struct {
	gate        chan struct{}
	dead        atomic.Bool
	fields      sync.Mutex
	cfg         ServerConfig
	client      *http.Client
	cmd         *exec.Cmd
	process     sessionProcess
	input       io.WriteCloser
	output      io.ReadCloser
	reader      *bufio.Reader
	httpID      string
	initialized bool
	next        int
	idle        *time.Timer
	cached      json.RawMessage
	cachedAt    time.Time
}

// sessionProcess owns the complete native process lifetime of a persistent
// stdio session.  It is intentionally separate from exec.Cmd: closing only
// the parent is not enough once a bridge has created an ADB/helper child.
type sessionProcess interface {
	terminate() error
}

func (s *mcpSession) close() error {
	s.dead.Store(true)
	s.fields.Lock()
	defer s.fields.Unlock()
	var closeErr error
	if s.input != nil {
		closeErr = errors.Join(closeErr, s.input.Close())
		s.input = nil
	}
	if s.output != nil {
		closeErr = errors.Join(closeErr, s.output.Close())
		s.output = nil
	}
	if s.client != nil {
		s.client.CloseIdleConnections()
		s.client = nil
	}
	if s.process != nil {
		if err := s.process.terminate(); err != nil {
			// Keep the process reference. A later Hub.Close may prove the native
			// tree exited; treating this as success would release a device lease
			// while a helper could still act.
			return errors.Join(closeErr, err)
		}
		s.process = nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		// terminate() confirmed no active tree members before this wait, so a
		// Wait here cannot hang behind a still-live bridge parent.
		if err := s.cmd.Wait(); err != nil {
			var exited *exec.ExitError
			if !errors.As(err, &exited) {
				closeErr = errors.Join(closeErr, err)
			}
		}
		s.cmd = nil
	}
	return closeErr
}

func (h *Hub) scopedRequest(ctx context.Context, c ServerConfig, scope, method string, params any, result *json.RawMessage) error {
	ctx, cancel := serverTimeout(ctx, c)
	defer cancel()
	key := c.ID + "\x00" + scope
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("MCP runtime closed")
	}
	s := h.sessions[key]
	if s == nil || s.dead.Load() {
		if s != nil {
			// A dead session may still own a native Job/process group. Do not
			// discard its reference and start another helper until termination is
			// confirmed by close().
			if err := s.close(); err != nil {
				h.mu.Unlock()
				return errors.New("MCP session cleanup unconfirmed; operation not started")
			}
			delete(h.sessions, key)
		}
		for oldKey, old := range h.sessions {
			if old.dead.Load() && old.close() == nil {
				delete(h.sessions, oldKey)
			}
		}
		if len(h.sessions) >= 32 {
			h.mu.Unlock()
			return errors.New("MCP session capacity reached")
		}
		s = &mcpSession{cfg: c, gate: make(chan struct{}, 1)}
		h.sessions[key] = s
	}
	h.mu.Unlock()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.gate }()
	if s.dead.Load() {
		return errors.New("MCP session ended; re-plan without repeating unknown operations")
	}
	if s.idle != nil {
		s.idle.Stop()
	}
	stop := context.AfterFunc(ctx, func() { _ = s.close() })
	defer stop()
	defer func() { s.idle = time.AfterFunc(5*time.Minute, func() { _ = s.close() }) }()
	if !s.initialized {
		if err := s.start(); err != nil {
			_ = s.close()
			return errors.New("MCP connection unavailable")
		}
		response, err := s.rpc(ctx, "initialize", initParams(), true)
		if err != nil {
			return sessionError(ctx, errors.Join(err, s.close()))
		}
		if err = rpcErr(response); err != nil {
			return errors.Join(err, s.close())
		}
		if _, err = s.rpc(ctx, "notifications/initialized", map[string]any{}, false); err != nil {
			return sessionError(ctx, errors.Join(err, s.close()))
		}
		s.initialized = true
	}
	firstListPage := method == "tools/list"
	if m, ok := params.(map[string]any); ok {
		if cursor, _ := m["cursor"].(string); cursor != "" {
			firstListPage = false
		}
	}
	if firstListPage && s.cached != nil && time.Since(s.cachedAt) < 30*time.Second {
		*result = append((*result)[:0], s.cached...)
		return nil
	}
	response, err := s.rpc(ctx, method, params, true)
	if err != nil {
		return sessionError(ctx, errors.Join(err, s.close()))
	}
	if err = rpcErr(response); err != nil {
		return err
	}
	*result = response.Result
	if firstListPage {
		s.cached = append([]byte(nil), response.Result...)
		s.cachedAt = time.Now()
	}
	return nil
}

func sessionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		// Preserve both caller cancellation and any failed native cleanup. A
		// caller may classify the operation as cancelled, but it must not assume
		// a device/plugin lease is safe to reuse when tree confirmation failed.
		return errors.Join(ctx.Err(), err)
	}
	return errors.Join(errors.New("MCP transport interrupted; operation outcome may be unknown, not replayed"), err)
}

func (s *mcpSession) start() error {
	if s.cfg.Transport == "http" {
		client, err := mcpClient(s.cfg.URL)
		if err != nil {
			return err
		}
		s.fields.Lock()
		defer s.fields.Unlock()
		if s.dead.Load() {
			client.CloseIdleConnections()
			return errors.New("MCP session closed")
		}
		s.client = client
		return nil
	}
	// The persistent child has no caller-owned lifetime. It is closed on failed
	// requests, runtime shutdown or idle expiry; credentials require explicit Env.
	cmd := exec.Command(s.cfg.Command, s.cfg.Args...)
	configureSessionProcess(cmd)
	for _, name := range []string{"PATH", "HOME", "USER", "LANG", "TZ", "TMPDIR", "TEMP", "TMP", "SystemRoot", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	for name, value := range s.cfg.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	s.fields.Lock()
	defer s.fields.Unlock()
	if s.dead.Load() {
		return errors.New("MCP session closed")
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return err
	}
	process, err := attachSessionProcess(cmd)
	if err != nil {
		// Assignment is a hard security boundary.  This direct parent cleanup
		// is only for a process that never received an RPC; do not continue via
		// the old taskkill/parent-only path.
		_ = in.Close()
		_ = out.Close()
		killErr := cmd.Process.Kill()
		waitErr := cmd.Wait()
		var exited *exec.ExitError
		if errors.As(waitErr, &exited) {
			waitErr = nil
		}
		return errors.Join(err, killErr, waitErr)
	}
	s.cmd, s.process, s.input, s.output = cmd, process, in, out
	s.reader = bufio.NewReaderSize(out, maxFrame+1)
	return nil
}

func (s *mcpSession) rpc(ctx context.Context, method string, params any, want bool) (rpcResponse, error) {
	id := 0
	if want {
		s.next++
		id = s.next
	}
	b, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil || len(b) > maxFrame {
		return rpcResponse{}, errors.New("MCP request too large")
	}
	// close() must clear its references after closing each transport so a later
	// retry can distinguish a confirmed shutdown. Snapshot everything rpc uses
	// under the same mutex, then perform I/O without that lock: Close can still
	// interrupt a blocked pipe/HTTP request, but cannot turn a field read into a
	// nil dereference between this check and Write/Do/readRPC.
	s.fields.Lock()
	dead := s.dead.Load()
	client := s.client
	input := s.input
	reader := s.reader
	httpID := s.httpID
	url := s.cfg.URL
	headers := s.cfg.Headers
	s.fields.Unlock()
	if dead {
		return rpcResponse{}, errors.New("MCP session closed")
	}
	if client == nil {
		if input == nil || reader == nil {
			return rpcResponse{}, errors.New("MCP session transport unavailable")
		}
		if _, err = input.Write(append(b, '\n')); err != nil {
			return rpcResponse{}, err
		}
		if !want {
			return rpcResponse{}, nil
		}
		return readRPC(reader, id)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return rpcResponse{}, err
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if httpID != "" {
		req.Header.Set("Mcp-Session-Id", httpID)
	}
	response, err := client.Do(req)
	if err != nil {
		return rpcResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return rpcResponse{}, fmt.Errorf("MCP HTTP status %d", response.StatusCode)
	}
	if value := response.Header.Get("Mcp-Session-Id"); value != "" {
		s.fields.Lock()
		if !s.dead.Load() {
			s.httpID = value
		}
		s.fields.Unlock()
	}
	if !want {
		return rpcResponse{}, nil
	}
	return readHTTPRPC(response.Body, id)
}
