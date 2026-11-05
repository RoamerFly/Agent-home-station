package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/agenthost"
)

func TestLocalRuntimeUsesOnlyPackagedFilesAndPrivateSpool(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "private")
	cfg, err := LocalWindowsRuntime(base, data, "")
	if err != nil || cfg != nil {
		t.Fatalf("uninstalled runtime: %#v %v", cfg, err)
	}
	for _, rel := range []string{"runtimes/windows-python-cli/pythonw.exe", "runtimes/windows-agent-host/windows_agent_host.pyw"} {
		path := filepath.Join(base, filepath.FromSlash(rel))
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("synthetic runtime fixture, never executed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err = LocalWindowsRuntime(base, data, "")
	if err != nil || cfg == nil || cfg.Spool != filepath.Join(data, "local-execution-spool") {
		t.Fatalf("packaged runtime: %#v %v", cfg, err)
	}
	file := filepath.Join(base, "runtime.json")
	cfg.Spool = filepath.Join(base, "untrusted-spool")
	content, _ := json.Marshal(cfg)
	if err = os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LocalWindowsRuntime(base, data, file)
	if err != nil || cfg.Spool != filepath.Join(data, "local-execution-spool") {
		t.Fatalf("runtime document moved control plane: %#v %v", cfg, err)
	}
}

func TestLocalRuntimeCannotBeEditedByTheFish(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	windows := &agenthost.WindowsConfig{Pythonw: filepath.Join(workspace, "pythonw.exe"), Helper: filepath.Join(base, "helper.pyw"), Spool: filepath.Join(base, "spool")}
	if handler, err := NewLocalHomeHandler(filepath.Join(base, "private"), workspace, "synthetic-password", "", windows); err == nil {
		handler.Close()
		t.Fatal("mutable host interpreter admitted")
	}
}

func TestLocalRuntimeRejectsMissingOrDirectoryInExplicitConfig(t *testing.T) {
	base := t.TempDir()
	cfg := agenthost.WindowsConfig{Pythonw: filepath.Join(base, "pythonw.exe"), Helper: filepath.Join(base, "helper.pyw")}
	file := filepath.Join(base, "runtime.json")
	content, _ := json.Marshal(cfg)
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		if runtime, err := LocalWindowsRuntime(base, filepath.Join(base, "private"), file); err == nil || runtime != nil {
			t.Fatalf("unusable explicit runtime accepted: %#v %v", runtime, err)
		}
	}
	check()
	if err := os.WriteFile(cfg.Pythonw, []byte("synthetic, never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	check()
	if err := os.Mkdir(cfg.Helper, 0700); err != nil {
		t.Fatal(err)
	}
	check()
}
