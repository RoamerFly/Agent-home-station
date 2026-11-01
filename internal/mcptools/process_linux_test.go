//go:build linux

package mcptools

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureSessionProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type processGroupSession struct{ pgid int }

func attachSessionProcess(cmd *exec.Cmd) (sessionProcess, error) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid < 1 {
		return nil, errors.New("MCP process unavailable")
	}
	// configureSessionProcess requested Setpgid. Verify the child is really
	// its own group before relying on a negative-PID kill tree boundary.
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil || pgid != cmd.Process.Pid {
		if err == nil {
			err = errors.New("MCP process group unavailable")
		}
		return nil, err
	}
	return &processGroupSession{pgid: pgid}, nil
}

func (p *processGroupSession) terminate() error {
	if p == nil || p.pgid < 1 {
		return errors.New("MCP process group unavailable")
	}
	pgid := p.pgid
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		// A successful group signal may leave zombies briefly. Keep the group
		// identity until the kernel confirms there are no remaining members.
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			p.pgid = 0
			return nil
		} else if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("MCP process group still active")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
