package agenthost

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireLinuxSandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux bubblewrap integration; Windows LPAC has separate native validation")
	}
	if _, e := exec.LookPath("bwrap"); e != nil {
		t.Skip("bubblewrap not installed")
	}
}
func TestLegacyShellFailsClosed(t *testing.T) {
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{t.TempDir()}
	b, _ := json.Marshal(Input{Path: c.Roots[0], Command: "echo forbidden"})
	if _, e := (Host{c}).Call(context.Background(), "server_exec", b); e == nil || !strings.Contains(e.Error(), "sandbox_required") {
		t.Fatal(e)
	}
}
func TestSandboxCannotReadOtherRootsOrHostServices(t *testing.T) {
	requireLinuxSandbox(t)
	inside, outside := t.TempDir(), t.TempDir()
	file := filepath.Join(outside, "secret")
	os.WriteFile(file, []byte("synthetic-secret"), 0600)
	os.Symlink(outside, filepath.Join(inside, "escape"))
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{inside}
	c.Isolation = &Isolation{}
	command := "test ! -e '" + file + "' && test ! -e '" + filepath.Join(inside, "escape", "secret") + "' && test ! -e /mnt/c && test ! -e /run/user && test ! -e /proc/1/root/home/fly && test -z \"$WSL_INTEROP$DEEPSEEK_API_KEY\" && printf ok > proof.txt"
	b, _ := json.Marshal(Input{Path: inside, Command: command})
	v, e := (Host{c}).Call(context.Background(), "server_exec", b)
	if e != nil || v.(map[string]any)["exit_code"] != 0 {
		t.Fatal(v, e)
	}
	if b, e := os.ReadFile(filepath.Join(inside, "proof.txt")); e != nil || string(b) != "ok" {
		t.Fatal(e)
	}
}
