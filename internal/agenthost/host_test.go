package agenthost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func invoke(t *testing.T, h Host, name string, v any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(v)
	return h.Call(context.Background(), name, b)
}
func TestHostFilePermissionsAndRootBoundary(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600)
	cfg := Defaults()
	cfg.Roots = []string{root}
	h := Host{cfg}
	if _, err := invoke(t, h, "server_status", map[string]string{}); err == nil {
		t.Fatal("disabled allowed")
	}
	h.Config.Mode = "read"
	path := filepath.Join(root, "note.txt")
	if _, err := invoke(t, h, "server_write", map[string]string{"path": path, "content": "hello"}); err == nil {
		t.Fatal("read wrote")
	}
	h.Config.Mode = "files"
	if _, err := invoke(t, h, "server_write", map[string]string{"path": path, "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, h, "server_write", map[string]string{"path": path, "content": "overwritten"}); err == nil {
		t.Fatal("silently overwrote")
	}
	v, err := invoke(t, h, "server_read", map[string]string{"path": path})
	if err != nil || v.(map[string]any)["content"] != "hello" {
		t.Fatal(v, err)
	}
	if _, err = invoke(t, h, "server_read", map[string]string{"path": filepath.Join(outside, "secret")}); err == nil {
		t.Fatal("outside read")
	}
	if err = os.Symlink(outside, filepath.Join(root, "escape")); err == nil {
		if _, err = invoke(t, h, "server_read", map[string]string{"path": filepath.Join(root, "escape", "secret")}); err == nil {
			t.Fatal("symlink escape")
		}
	}
	if _, err = invoke(t, h, "server_exec", map[string]string{"path": root, "command": "echo unauthorized"}); err == nil {
		t.Fatal("files ran command")
	}
}
func TestHostFullCommandBoundsAndNoCredentialEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "must-not-forward")
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{t.TempDir()}
	h := Host{c}
	command := "printf '%s' \"${DEEPSEEK_API_KEY-unset}\""
	if runtime.GOOS == "windows" {
		command = "if ($env:DEEPSEEK_API_KEY) { Write-Output $env:DEEPSEEK_API_KEY } else { Write-Output 'unset' }"
	}
	v, err := invoke(t, h, "server_exec", map[string]string{"path": c.Roots[0], "command": command})
	if err != nil {
		t.Fatal(err)
	}
	data := v.(map[string]any)
	if data["exit_code"] != 0 || !strings.Contains(data["output"].(string), "unset") {
		t.Fatal(data)
	}
}
func TestHostCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux process group check")
	}
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	b, _ := json.Marshal(Input{Path: c.Roots[0], Command: "sleep 30 & wait"})
	start := time.Now()
	v, err := (Host{c}).Call(ctx, "server_exec", b)
	if err != nil || time.Since(start) > 3*time.Second || !v.(map[string]any)["cancelled"].(bool) {
		t.Fatal(v, err, time.Since(start))
	}
}
