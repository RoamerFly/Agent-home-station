//go:build windows

package pluginruntime

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func isolatedCommand(s LaunchSpec, argv []string) (*exec.Cmd, func(), error) {
	noop := func() {}
	if !strings.EqualFold(filepath.Base(s.Pythonw), "pythonw.exe") || filepath.Base(s.WindowsHelper) != "windows_plugin_host.pyw" {
		return nil, noop, errors.New("Windows plugin broker unavailable")
	}
	for _, path := range []string{s.Pythonw, s.WindowsHelper, filepath.Join(filepath.Dir(s.WindowsHelper), "windows_lpac.py")} {
		if err := cleanPath(path, false); err != nil {
			return nil, noop, err
		}
	}
	f, err := os.CreateTemp("", "wechat-plugin-launch-*.json")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	// The document lives outside all plugin mounts; no control-plane credentials
	// or host environment are inherited by the child.
	doc := map[string]any{"argv": argv, "cwd": s.DataDir, "writable": []string{s.DataDir}, "read_only": readOnlyRoots(s), "parent_pid": os.Getpid()}
	err = json.NewEncoder(f).Encode(doc)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	cmd := exec.Command(s.Pythonw, s.WindowsHelper, f.Name())
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	// The trusted broker needs Windows system discovery only. Never inherit API
	// tokens/proxy variables/PYTHONPATH into it or the LPAC child.
	for _, name := range []string{"SystemRoot", "WINDIR", "SystemDrive", "ProgramData", "TEMP", "TMP"} {
		if v := os.Getenv(name); v != "" {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
	}
	return cmd, cleanup, nil
}
func terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
