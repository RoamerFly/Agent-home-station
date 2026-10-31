//go:build windows

package adbserver

import (
	"os/exec"
	"syscall"
)

// configureVersionCommand prevents the short, host-owned client preflight
// from flashing a console window when launched by the desktop application.
func configureVersionCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
