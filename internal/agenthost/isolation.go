package agenthost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Isolation is an execution policy, not a suggestion to the model. ReadOnly
// mounts are deliberately separate from file-tool roots.
type Isolation struct {
	GPU      bool     `json:"gpu,omitempty"`
	ReadOnly []string `json:"read_only,omitempty"`
}

func isolatedCommand(ctx context.Context, cfg Config, path, command string) (*exec.Cmd, error) {
	if cfg.Isolation == nil {
		return nil, errors.New("sandbox_required: legacy unrestricted execution is disabled")
	}
	if runtime.GOOS != "linux" {
		return nil, errors.New("sandbox_unavailable: use the isolated Linux executor; unrestricted host fallback is forbidden")
	}
	binary, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, errors.New("sandbox_unavailable: bubblewrap is not installed")
	}
	args := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--clearenv"}
	// An empty mount namespace: no host /home, /mnt, /run, network, inherited
	// credentials, Docker/DBus socket or WSL executable/interop is exposed.
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if info, e := os.Lstat(dir); e == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				target, e := os.Readlink(dir)
				if e != nil {
					return nil, e
				}
				args = append(args, "--symlink", target, dir)
			} else {
				args = append(args, "--ro-bind", dir, dir)
			}
		}
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", "/home/agent", "--setenv", "HOME", "/home/agent", "--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin", "--setenv", "LANG", "C.UTF-8", "--setenv", "TMPDIR", "/tmp")
	args = append(args, "--tmpfs", "/dev/shm")
	if info, e := os.Lstat("/etc/ld.so.cache"); e == nil && info.Mode().IsRegular() {
		args = append(args, "--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache")
	}
	// Library sonames can traverse /etc/alternatives on Debian/Ubuntu. Only
	// recreate library links into already mounted system libraries, never /etc.
	aliases, _ := filepath.Glob("/etc/alternatives/lib*.so*")
	var links []string
	for _, alias := range aliases {
		target, e := os.Readlink(alias)
		if e != nil {
			continue
		}
		resolved, e := filepath.EvalSymlinks(alias)
		if e != nil || !(strings.HasPrefix(resolved, "/usr/lib/") || strings.HasPrefix(resolved, "/lib/")) {
			continue
		}
		links = append(links, "--symlink", target, alias)
	}
	if len(links) > 0 {
		args = append(args, "--dir", "/etc/alternatives")
		args = append(args, links...)
	}
	if cfg.Isolation.GPU {
		found := false
		for _, device := range []string{"/dev/dxg", "/dev/nvidia0", "/dev/nvidiactl", "/dev/nvidia-uvm", "/dev/nvidia-uvm-tools"} {
			if stat, e := os.Stat(device); e == nil && stat.Mode()&os.ModeDevice != 0 {
				args = append(args, "--dev-bind", device, device)
				found = true
			}
		}
		if !found {
			return nil, errors.New("gpu_device_unavailable")
		}
		args = append(args, "--setenv", "LD_LIBRARY_PATH", "/usr/lib/wsl/lib:/usr/lib/x86_64-linux-gnu")
	}
	// Do not permit roots that would expose a host filesystem or control plane.
	for _, base := range append(append([]string{}, cfg.Isolation.ReadOnly...), cfg.Roots...) {
		clean := filepath.Clean(base)
		if !filepath.IsAbs(base) || clean != base || clean == "/" || clean == "/mnt" || clean == "/home" || clean == "/run" || strings.HasPrefix(clean, "/proc") || strings.HasPrefix(clean, "/dev") {
			return nil, errors.New("sandbox_invalid_mount")
		}
		resolved, e := filepath.EvalSymlinks(base)
		if e != nil || resolved != base {
			return nil, errors.New("sandbox_mount_symlink_or_missing")
		}
	}
	for _, base := range cfg.Isolation.ReadOnly {
		args = append(args, "--ro-bind", base, base)
	}
	for _, base := range cfg.Roots {
		args = append(args, "--bind", base, base)
	}
	args = append(args, "--chdir", path, "--", "/bin/sh", "-c", command)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.WaitDelay = time.Second
	configureProcess(cmd)
	return cmd, nil
}
