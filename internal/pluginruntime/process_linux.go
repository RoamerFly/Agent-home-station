//go:build linux

package pluginruntime

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func isolatedCommand(s LaunchSpec, argv []string) (*exec.Cmd, func(), error) {
	noop := func() {}
	binary, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, noop, errors.New("plugin sandbox unavailable: bubblewrap required")
	}
	args := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--clearenv"}
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		info, e := os.Lstat(dir)
		if e != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(dir)
			if e != nil {
				return nil, noop, e
			}
			args = append(args, "--symlink", target, dir)
		} else {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/dev/shm", "--setenv", "HOME", s.DataDir, "--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin", "--setenv", "LANG", "C.UTF-8")
	if info, e := os.Stat("/etc/ld.so.cache"); e == nil && info.Mode().IsRegular() {
		args = append(args, "--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache")
	}
	for _, dir := range append([]string{s.PackageDir}, s.RuntimeRoots...) {
		args = append(args, "--ro-bind", dir, dir)
	}
	args = append(args, "--bind", s.DataDir, s.DataDir, "--chdir", s.DataDir, "--")
	args = append(args, argv...)
	cmd := exec.Command(binary, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, noop, nil
}
func terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
