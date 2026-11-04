//go:build !windows

package transcript

import "os/exec"

func hideWindow(*exec.Cmd) {}
