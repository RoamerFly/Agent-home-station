package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Only tests may opt into this synthetic non-sandbox launcher. Production has
// no equivalent fallback. It can run only four fixtures in the current test
// binary, and keeps the existing native process-tree cancellation assertions.
func syntheticStdioLauncher(ctx context.Context, c ServerConfig) (StdioProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Command != os.Args[0] || len(c.Args) != 1 {
		return nil, errors.New("synthetic test binary required")
	}
	allowed := false
	for _, name := range []string{"TestHelperMCPProcess", "TestPersistentMCPChild", "TestMCPProcessTreeFixture", "TestInheritedMCPPipeProcess"} {
		if strings.Trim(c.Args[0], "^$") == "-test.run="+name || c.Args[0] == "-test.run=^"+name+"$" {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("unregistered synthetic test entrypoint")
	}
	cmd := exec.Command(c.Command, c.Args...)
	configureSessionProcess(cmd)
	for _, name := range []string{"SystemRoot", "WINDIR", "PATH"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	for name, value := range c.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, err
	}
	process, err := attachSessionProcess(cmd)
	if err != nil {
		input.Close()
		output.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	stream := &syntheticStdioProcess{input: input, output: output, process: process, done: make(chan struct{})}
	go func() { stream.waitErr = cmd.Wait(); close(stream.done) }()
	return stream, nil
}

type syntheticStdioProcess struct {
	input   io.WriteCloser
	output  io.ReadCloser
	process sessionProcess
	done    chan struct{}
	waitErr error
	mu      sync.Mutex
}

func (s *syntheticStdioProcess) Stdin() io.WriteCloser { return s.input }
func (s *syntheticStdioProcess) Stdout() io.ReadCloser { return s.output }
func (s *syntheticStdioProcess) Wait() error           { <-s.done; return s.waitErr }
func (s *syntheticStdioProcess) Close() error {
	s.mu.Lock()
	_ = s.input.Close()
	_ = s.output.Close()
	if s.process != nil {
		if err := s.process.terminate(); err != nil {
			s.mu.Unlock()
			return err
		}
		s.process = nil
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("synthetic cleanup unconfirmed")
	}
}

func TestStdioWithoutRegisteredLauncherFailsClosedInEveryScope(t *testing.T) {
	config := ServerConfig{ID: "unregistered", Transport: "stdio", Enabled: true, Command: os.Args[0], Args: []string{"-test.run=TestHelperMCPProcess"}, Env: map[string]string{"GO_WANT_MCP_HELPER": "1"}}
	hub, err := New([]ServerConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	for _, ctx := range []context.Context{t.Context(), WithSessionScope(t.Context(), "account-thread")} {
		if _, err := hub.List(ctx); err == nil || !strings.Contains(err.Error(), "sandbox_required") {
			t.Fatal("stdio escaped required launcher", err)
		}
		if _, err := hub.Call(ctx, "unregistered__echo", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "sandbox_required") {
			t.Fatal("stdio call escaped required launcher", err)
		}
	}
}

func TestStdioUsesOnlyHostInjectedRuntimeAndPreservesLauncherRefusal(t *testing.T) {
	config := ServerConfig{ID: "registered", Transport: "stdio", Enabled: true, Command: "untrusted-config-command", Args: []string{"must-not-execute"}, Env: map[string]string{"SECRET": "must-not-forward"}}
	fixture := ServerConfig{Command: os.Args[0], Args: []string{"-test.run=TestHelperMCPProcess"}, Env: map[string]string{"GO_WANT_MCP_HELPER": "1"}}
	launches := 0
	hub, err := New([]ServerConfig{config}, WithStdioLauncher(func(ctx context.Context, _ ServerConfig) (StdioProcess, error) {
		launches++
		return syntheticStdioLauncher(ctx, fixture)
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	for _, ctx := range []context.Context{t.Context(), WithSessionScope(t.Context(), "account-thread")} {
		out, err := hub.Call(ctx, "registered__echo", json.RawMessage(`{}`))
		if err != nil || out != "stdio result" {
			t.Fatal(out, err)
		}
	}
	if launches != 2 {
		t.Fatal("wrong runtime routing", launches)
	}
	refused := errors.New("synthetic registration revoked")
	denied, _ := New([]ServerConfig{config}, WithStdioLauncher(func(context.Context, ServerConfig) (StdioProcess, error) { return nil, refused }))
	defer denied.Close()
	if _, err := denied.Call(t.Context(), "registered__echo", json.RawMessage(`{}`)); !errors.Is(err, refused) {
		t.Fatal("launcher refusal downgraded to raw exec", err)
	}
}

func TestInstalledStdioConfigurationRejectsAmbiguousLaunchAuthority(t *testing.T) {
	base := ServerConfig{ID: "registered", Enabled: true, Transport: "stdio", PluginID: "installed-server"}
	if _, err := New([]ServerConfig{base}); err != nil {
		t.Fatal("installed runtime rejected without raw command", err)
	}
	implicit := base
	implicit.Transport = ""
	if hub, err := New([]ServerConfig{implicit}); err != nil {
		t.Fatal("omitted installed stdio transport rejected", err)
	} else if hub.servers[0].Transport != "stdio" || hub.servers[0].TimeoutSeconds != 0 {
		t.Fatal("unexpected transport/timeout normalization", hub.servers[0])
	}
	for _, mutate := range []func(*ServerConfig){
		func(c *ServerConfig) { c.PluginID = "../server" },
		func(c *ServerConfig) { c.Command = "python" },
		func(c *ServerConfig) { c.Args = []string{"main.py"} },
		func(c *ServerConfig) { c.Env = map[string]string{"SECRET": "synthetic"} },
		func(c *ServerConfig) { c.Transport = "http"; c.URL = "http://127.0.0.1:1" },
		func(c *ServerConfig) {
			c.Enabled = false
			c.Transport = "streamable-http"
			c.URL = "http://127.0.0.1:1"
		},
	} {
		candidate := base
		mutate(&candidate)
		if _, err := New([]ServerConfig{candidate}); err == nil {
			t.Fatal("ambiguous or invalid registered stdio configuration admitted", candidate)
		}
	}
	legacy := base
	legacy.PluginID = ""
	legacy.Command = "legacy-command"
	if _, err := New([]ServerConfig{legacy}); err != nil {
		t.Fatal("legacy configuration unreadable before migration", err)
	}
	hub, err := New([]ServerConfig{base})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	if _, err := hub.List(t.Context()); err == nil || !strings.Contains(err.Error(), "sandbox_required") {
		t.Fatal("plugin_id bypassed host runtime resolution", err)
	}
}

func TestServerGuardRevocationClosesAllServerScopesBeforeCachedRequest(t *testing.T) {
	config := ServerConfig{ID: "fixture", Enabled: true, Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestPersistentMCPChild$"}, Env: map[string]string{"WECHAT_MCP_SESSION_CHILD": "1"}}
	var revoked atomic.Bool
	hub, err := New([]ServerConfig{config}, WithStdioLauncher(syntheticStdioLauncher), WithServerGuard(func(context.Context, ServerConfig) error {
		if revoked.Load() {
			return errors.New("synthetic authority revoked")
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	for _, scope := range []string{"a", "b"} {
		if _, err = hub.List(WithSessionScope(t.Context(), scope)); err != nil {
			t.Fatal(err)
		}
	}
	if len(hub.sessions) != 2 {
		t.Fatal("missing scoped sessions")
	}
	revoked.Store(true)
	if _, err = hub.List(WithSessionScope(t.Context(), "a")); err == nil {
		t.Fatal("cached discovery retained revoked authority")
	}
	if len(hub.sessions) != 0 {
		t.Fatal("revocation did not close every server scope")
	}
	if _, err = hub.Call(WithSessionScope(t.Context(), "b"), "fixture__increment", json.RawMessage(`{}`)); err == nil {
		t.Fatal("revocation silently respawned old runtime")
	}
}
