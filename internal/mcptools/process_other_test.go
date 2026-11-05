//go:build !linux && !windows

package mcptools

import (
	"errors"
	"os"
	"os/exec"
)

func configureSessionProcess(cmd *exec.Cmd) {}

// Unsupported Unix-like hosts do not have a portable process-group contract
// in the standard library. Fail closed at shutdown if the direct child cannot
// be terminated; callers will not silently regard the session as reusable.
type directSessionProcess struct{ process *os.Process }

func attachSessionProcess(cmd *exec.Cmd) (sessionProcess, error) {
	if cmd == nil || cmd.Process == nil {
		return nil, errors.New("MCP process unavailable")
	}
	return &directSessionProcess{process: cmd.Process}, nil
}

func (p *directSessionProcess) terminate() error {
	if p == nil || p.process == nil {
		return errors.New("MCP process unavailable")
	}
	process := p.process
	p.process = nil
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
