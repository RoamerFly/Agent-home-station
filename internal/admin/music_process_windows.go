//go:build windows

package admin

import (
	"os/exec"
	"syscall"
)

func configureMusicProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
