package mcptools

import (
	"context"
	"errors"
	"io"
)

// StdioProcess is owned by a trusted mandatory-sandbox launcher. Close must
// interrupt pipes, stop the complete sandbox tree, and confirm termination.
type StdioProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Wait() error
	Close() error
}

// StdioLauncher is supplied by host code after resolving an approved installed
// package/runtime. ServerConfig is identification/configuration intent, never
// permission to exec its raw Command, Args or Env outside that registration.
type StdioLauncher func(context.Context, ServerConfig) (StdioProcess, error)
type Option func(*Hub)

// ServerGuard revalidates host authority before every request, including
// cached discovery on an existing session. Revocation closes that server's
// sessions; the guard is host code and cannot be supplied by JSON settings.
type ServerGuard func(context.Context, ServerConfig) error

func WithServerGuard(guard ServerGuard) Option { return func(h *Hub) { h.serverGuard = guard } }

// SessionScope exposes only the existing host-bound scope, never a path.
func SessionScope(ctx context.Context) string {
	scope, _ := ctx.Value(sessionScopeKey{}).(string)
	return scope
}

func WithStdioLauncher(launcher StdioLauncher) Option {
	return func(h *Hub) { h.stdioLauncher = launcher }
}

func launchStdio(ctx context.Context, c ServerConfig, launcher StdioLauncher) (StdioProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if launcher == nil {
		return nil, errors.New("sandbox_required: registered MCP stdio launcher unavailable")
	}
	p, err := launcher(ctx, c)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("sandbox_required: MCP stdio launcher returned no process")
	}
	if p.Stdin() == nil || p.Stdout() == nil {
		_ = p.Close()
		return nil, errors.New("sandbox_required: MCP stdio streams unavailable")
	}
	return p, nil
}

type stdioSessionProcess struct{ StdioProcess }

func (p stdioSessionProcess) terminate() error { return p.Close() }
