//go:build !windows

package admin

import "os/exec"

func configureMusicProcess(command *exec.Cmd) {}
