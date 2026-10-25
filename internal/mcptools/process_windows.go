//go:build windows

package mcptools

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func configureSessionProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000200}
}

func killSessionProcess(cmd *exec.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	kill := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if kill.Run() != nil {
		_ = cmd.Process.Kill()
	}
}
