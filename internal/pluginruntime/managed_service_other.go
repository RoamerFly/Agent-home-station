//go:build !windows

package pluginruntime

import "os/exec"

func configureManagedService(command *exec.Cmd) {}
