//go:build !windows

package adbserver

import "os/exec"

func configureVersionCommand(_ *exec.Cmd) {}
