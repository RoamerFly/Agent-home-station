//go:build !linux && !windows

package agenthost

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}
