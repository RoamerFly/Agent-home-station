//go:build windows

package pluginruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

// The intermediate host exits without calling Close, simulating a crash.
func TestSyntheticPluginParent(t *testing.T) {
	if os.Getenv("WECHAT_PLUGIN_PARENT_FIXTURE") != "1" {
		return
	}
	var spec LaunchSpec
	if json.Unmarshal([]byte(os.Getenv("WECHAT_PLUGIN_PARENT_SPEC")), &spec) != nil {
		os.Exit(2)
	}
	p, err := pluginhost.Verify(spec.PackageDir)
	if err != nil {
		os.Exit(3)
	}
	e := &Executor{Resolve: func(context.Context, pluginhost.Package, pluginhost.Grant) (LaunchSpec, error) { return spec, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := e.Start(ctx, p, pluginhost.Grant{Account: "a", Plugin: p.ID, Version: p.Version})
	if err != nil {
		os.Exit(4)
	}
	raw, err := s.Call(ctx, invocation(), "pid", json.RawMessage(`{}`))
	if err != nil {
		os.Exit(5)
	}
	var child struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(raw, &child) != nil || child.PID <= 0 {
		os.Exit(6)
	}
	result, _ := json.Marshal(map[string]int{"broker": s.(*session).cmd.Process.Pid, "plugin": child.PID})
	fmt.Println(string(result))
	os.Exit(0)
}

func TestLivePluginDiesWithParent(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in Windows orphan prevention test")
	}
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	code, data := t.TempDir(), t.TempDir()
	readyFixture(t, code)
	spec := LaunchSpec{PackageDir: code, DataDir: data, Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}
	raw, _ := json.Marshal(spec)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSyntheticPluginParent$")
	cmd.Env = append(os.Environ(), "WECHAT_PLUGIN_PARENT_FIXTURE=1", "WECHAT_PLUGIN_PARENT_SPEC="+string(raw))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("synthetic parent failed: %v", err)
	}
	var pids map[string]int
	if json.Unmarshal(out, &pids) != nil || len(pids) != 2 {
		t.Fatal("missing process evidence")
	}
	for name, pid := range pids {
		h, err := syscall.OpenProcess(0x00100000, false, uint32(pid))
		if err == syscall.Errno(87) {
			continue
		} // no longer a live process
		if err != nil {
			t.Fatalf("cannot verify %s: %v", name, err)
		}
		state, e := syscall.WaitForSingleObject(h, 5000)
		syscall.CloseHandle(h)
		if e != nil || state != 0 {
			p, _ := os.FindProcess(pid)
			if p != nil {
				_ = p.Kill()
			}
			t.Fatalf("orphaned %s after host exit: %d %v", name, state, e)
		}
	}
}
