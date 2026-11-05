package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/agenthost"
)

func TestLiveLocalHomeWindowsPersistentToolAndRevocation(t *testing.T) {
	file := os.Getenv("AHS_WINDOWS_RUNTIME_CONFIG")
	if runtime.GOOS != "windows" || file == "" {
		t.Skip("opt-in synthetic local-home LPAC integration")
	}
	f := localChatFixture(t)
	windows, err := LocalWindowsRuntime("", f.dir, file)
	if err != nil {
		t.Fatal(err)
	}
	f.handler.chat.windows = windows
	store, input, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	tools := f.handler.chat.tools(store, input)
	call := func(tool string, in any) string {
		t.Helper()
		raw, _ := json.Marshal(in)
		out, err := tools.Call(context.Background(), tool, raw)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return out
	}
	_, state := f.request("GET", "/api/local/home", nil, true)
	home := state["directory"].(string)
	private := filepath.Join(f.dir, "synthetic-secret.txt")
	if err = os.WriteFile(private, []byte("host-only-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	code := "import pathlib,sys,time\nroot=pathlib.Path.cwd()\nif sys.argv[1]=='wait':\n (root/'started.txt').write_text('started')\n time.sleep(90)\n (root/'must-not-exist.txt').write_text('late')\nelse:\n try:\n  pathlib.Path(sys.argv[2]).read_text()\n except PermissionError:\n  pass\n else:\n  raise SystemExit('host-secret-readable')\n (root/'works'/'result.txt').write_text('persistent-tool-ok',encoding='utf-8')\n print('boundaries-preserved')\n"
	call("server_write", map[string]any{"path": "Project Tools/probe.py", "content": code})
	python := filepath.Join(filepath.Dir(windows.Pythonw), "python.exe")
	script := filepath.Join(home, "Project Tools", "probe.py")
	start := func(key, mode string) agenthost.Process {
		out := call("server_windows_process", map[string]any{"action": "start", "request_key": key, "path": home, "argv": []string{python, "-X", "utf8", script, mode, private}, "timeout_seconds": 120})
		var process agenthost.Process
		if err = json.Unmarshal([]byte(out), &process); err != nil || process.ID == "" {
			t.Fatal("process did not start", out, err)
		}
		return process
	}
	wait := func(id string, terminal bool) agenthost.Process {
		deadline := time.Now().Add(35 * time.Second)
		for time.Now().Before(deadline) {
			out := call("server_windows_process", map[string]any{"action": "status", "id": id})
			var process agenthost.Process
			if err = json.Unmarshal([]byte(out), &process); err != nil {
				t.Fatal(err)
			}
			if terminal && process.State != "running" {
				return process
			}
			if !terminal {
				if _, err = os.Stat(filepath.Join(home, "started.txt")); err == nil {
					return process
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("synthetic LPAC process did not reach expected state")
		return agenthost.Process{}
	}
	first := start("persistent-probe", "inspect")
	done := wait(first.ID, true)
	if done.State != "completed" || !strings.Contains(done.Output, "boundaries-preserved") {
		t.Fatalf("LPAC probe failed: %#v", done)
	}
	if again := start("persistent-probe", "inspect"); again.ID != first.ID {
		t.Fatal("same request submitted twice")
	}
	result := call("server_read", map[string]any{"path": "works/result.txt"})
	if !strings.Contains(result, "persistent-tool-ok") {
		t.Fatal("result was not in the fish home", result)
	}
	running := start("cancel-probe", "wait")
	wait(running.ID, false)
	if status, _ := f.request("POST", "/api/local/home", map[string]any{"action": "pause", "revision": state["revision"]}, true); status != 200 {
		t.Fatal(status)
	}
	cancelled := wait(running.ID, true)
	if cancelled.State != "cancelled" {
		t.Fatalf("revoked process continued: %#v", cancelled)
	}
	if _, err = os.Stat(filepath.Join(home, "must-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatal("late result written after cancellation", err)
	}
	f.server.Close()
	f.handler.Close()
	restart := newLocalHomeFixture(t, f.dir, f.root)
	restart.login()
	restart.handler.chat.windows = windows
	s, i, err := restart.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	retained := restart.handler.chat.tools(s, i)
	raw, _ := json.Marshal(map[string]any{"action": "status", "id": first.ID})
	out, err := retained.Call(context.Background(), "server_windows_process", raw)
	if err != nil || !strings.Contains(out, first.ID) || !strings.Contains(out, "completed") {
		t.Fatal("restart lost finished job", out, err)
	}
}
