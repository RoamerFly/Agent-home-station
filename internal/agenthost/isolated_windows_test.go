package agenthost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestLiveWindowsIsolatedArgv(t *testing.T) {
	path := os.Getenv("WECHAT_WINDOWS_TEST_CONFIG")
	if runtime.GOOS != "windows" || path == "" {
		t.Skip("opt-in Windows LPAC broker")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var windows WindowsConfig
	if json.Unmarshal(b, &windows) != nil {
		t.Fatal("invalid broker config")
	}
	cfg := Defaults()
	cfg.Mode = "full"
	cfg.Windows = &windows
	cwd := t.TempDir()
	cfg.Roots = []string{cwd}
	readonly, outside := t.TempDir(), filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("synthetic-private-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Isolation = &Isolation{ReadOnly: []string{filepath.Dir(windows.Pythonw), readonly}}
	args := []string{`literal %COMSPEC% & "中文" $()`, "", "trailing path\\"}
	argv := append([]string{filepath.Join(filepath.Dir(windows.Pythonw), "python.exe"), "-X", "utf8", "-c", "import json,sys;sys.stderr.write('private diagnostic');print(json.dumps(sys.argv[1:],ensure_ascii=False))"}, args...)
	out, err := RunIsolated(t.Context(), cfg, cwd, argv)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if json.Unmarshal(out, &got) != nil || !reflect.DeepEqual(args, got) {
		t.Fatal("argv changed or stderr mixed into structured stdout")
	}
	code := "import pathlib,sys\nfor p in sys.argv[1:]:\n try:\n  pathlib.Path(p).write_text('forbidden')\n except PermissionError: pass\n else: raise SystemExit('outside write permitted')\ntry:\n pathlib.Path(sys.argv[2]).read_text()\nexcept PermissionError: pass\nelse: raise SystemExit('outside read permitted')\nprint('boundaries_preserved')"
	probe := []string{argv[0], "-X", "utf8", "-c", code, filepath.Join(readonly, "escape.txt"), outside}
	if out, err = RunIsolated(t.Context(), cfg, cwd, probe); err != nil || string(out) != "boundaries_preserved\r\n" && string(out) != "boundaries_preserved\n" {
		t.Fatal("Python mount boundaries failed", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = RunIsolated(cancelled, cfg, cwd, argv); err != context.Canceled {
		t.Fatal("cancelled broker started", err)
	}
}
