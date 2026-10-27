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
	once        sync.Once
	fields      sync.Mutex
	cfg         ServerConfig
	client      *http.Client
	cmd         *exec.Cmd
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

func (s *mcpSession) close() {
	s.once.Do(func() {
		s.dead.Store(true)
		s.fields.Lock()
		defer s.fields.Unlock()
		if s.input != nil {
			_ = s.input.Close()
		}
		if s.output != nil {
			_ = s.output.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			killSessionProcess(s.cmd)
			go func() { _ = s.cmd.Wait() }()
		}
		if s.client != nil {
			s.client.CloseIdleConnections()
		}
	})
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
		for key, old := range h.sessions {
			if old.dead.Load() {
				delete(h.sessions, key)
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
	stop := context.AfterFunc(ctx, s.close)
	defer stop()
	defer func() { s.idle = time.AfterFunc(5*time.Minute, s.close) }()
	if !s.initialized {
		if err := s.start(); err != nil {
			s.close()
			return errors.New("MCP connection unavailable")
		}
		response, err := s.rpc(ctx, "initialize", initParams(), true)
		if err != nil {
			s.close()
			return sessionError(ctx, err)
		}
		if err = rpcErr(response); err != nil {
			s.close()
			return err
		}
		if _, err = s.rpc(ctx, "notifications/initialized", map[string]any{}, false); err != nil {
			s.close()
			return sessionError(ctx, err)
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
		s.close()
		return sessionError(ctx, err)
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
		return ctx.Err()
	}
	return errors.New("MCP transport interrupted; operation outcome may be unknown, not replayed")
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
	s.fields.Lock()
	defer s.fields.Unlock()
	if s.dead.Load() {
		in.Close()
		out.Close()
		killSessionProcess(cmd)
		go func() { _ = cmd.Wait() }()
		return errors.New("MCP session closed")
	}
	s.cmd, s.input, s.output = cmd, in, out
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
	if s.client == nil {
		if _, err = s.input.Write(append(b, '\n')); err != nil {
			return rpcResponse{}, err
		}
		if !want {
			return rpcResponse{}, nil
		}
		return readRPC(s.reader, id)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.cfg.URL, bytes.NewReader(b))
	if err != nil {
		return rpcResponse{}, err
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	for key, value := range s.cfg.Headers {
		req.Header.Set(key, value)
	}
	if s.httpID != "" {
		req.Header.Set("Mcp-Session-Id", s.httpID)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return rpcResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return rpcResponse{}, fmt.Errorf("MCP HTTP status %d", response.StatusCode)
	}
	if value := response.Header.Get("Mcp-Session-Id"); value != "" {
		s.httpID = value
	}
	if !want {
		return rpcResponse{}, nil
	}
	return readHTTPRPC(response.Body, id)
}
