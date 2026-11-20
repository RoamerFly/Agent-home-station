//go:build !windows

package mediadecode

import "os/exec"

func hideDecoderWindow(*exec.Cmd) {}

func decoderPath(path string) string { return path }
