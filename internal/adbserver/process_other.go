//go:build !windows

package adbserver

import (
	"os/exec"
	"sync"
	"time"
)

type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func platformStart(cmd *exec.Cmd) (Process, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() { err := cmd.Wait(); p.mu.Lock(); p.err = err; p.mu.Unlock(); close(p.done) }() // exactly one child reaper
	return p, nil
}
func (p *execProcess) Close() error {
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
		return nil
	case <-time.After(2 * time.Second):
		return errCleanupUnconfirmed
	}
}

func (p *execProcess) reaped() bool {
	if p == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *execProcess) waitError() error { p.mu.Lock(); defer p.mu.Unlock(); return p.err }
