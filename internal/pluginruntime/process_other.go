//go:build !windows && !linux

package pluginruntime

import (
	"errors"
	"os/exec"
)

func isolatedCommand(LaunchSpec, []string) (*exec.Cmd, func(), error) {
	return nil, func() {}, errors.New("plugin sandbox unsupported platform")
}
func terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
