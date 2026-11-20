package mediadecode

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func hideDecoderWindow(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// The bundled FFmpeg can open extended Windows paths, even when a plain
// path to the same file exceeds the legacy Win32 path limit.
func decoderPath(path string) string {
	if len(path) < 248 || !filepath.IsAbs(path) || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	path = filepath.Clean(path)
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}
