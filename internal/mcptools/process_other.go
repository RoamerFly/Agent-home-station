//go:build !linux && !windows

package mcptools

import "os/exec"

func configureSessionProcess(cmd *exec.Cmd) {}
func killSessionProcess(cmd *exec.Cmd)      { _ = cmd.Process.Kill() }
