//go:build windows

package pluginruntime

import (
	"os/exec"
	"syscall"
)

func configureManagedService(command *exec.Cmd) {
	// Do not break away from the desktop parent's kill-on-close Job Object.
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
