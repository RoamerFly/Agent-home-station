package agenthost

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
func TestSandboxSystemMediaLibrariesWithoutHostConfiguration(t *testing.T) {
	requireLinuxSandbox(t)
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg not installed")
	}
	inside := t.TempDir()
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{inside}
	c.Isolation = &Isolation{}
	b, _ := json.Marshal(Input{Path: inside, Command: "ffmpeg -version >/dev/null && test ! -e /etc/passwd && test ! -e /etc/shadow"})
	v, e := (Host{c}).Call(context.Background(), "server_exec", b)
	if e != nil || v.(map[string]any)["exit_code"] != 0 {
		t.Fatal(v, e)
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

func TestSandboxChildCannotConnectToLiveHostLoopback(t *testing.T) {
	requireLinuxSandbox(t)
	if _, e := exec.LookPath("python3"); e != nil {
		t.Skip("python3 is required for socket/subprocess acceptance")
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{t.TempDir()}
	c.Isolation = &Isolation{}
	// The outer isolated process launches a second Python process. Success
	// requires the socket probe to execute, fail to reach a live host listener,
	// and report the expected marker (rather than merely lacking Python).
	script := fmt.Sprintf("import socket,sys\ns=socket.socket();s.settimeout(0.5)\ntry:\n s.connect((\"127.0.0.1\",%d))\nexcept OSError:\n print(\"host-loopback-blocked\");sys.exit(0)\nsys.exit(4)", port)
	child, _ := json.Marshal(script)
	command := "python3 -c 'import subprocess,sys; sys.exit(subprocess.run([\"python3\",\"-c\"," + string(child) + "]).returncode)'"
	raw, _ := json.Marshal(Input{Path: c.Roots[0], Command: command})
	v, e := (Host{c}).Call(context.Background(), "server_exec", raw)
	if e != nil || v.(map[string]any)["exit_code"] != 0 || !strings.Contains(v.(map[string]any)["output"].(string), "host-loopback-blocked") {
		t.Fatal(v, e)
	}
}
