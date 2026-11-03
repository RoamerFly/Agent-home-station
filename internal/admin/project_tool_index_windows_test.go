package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/agenthost"
)

// This is an opt-in desktop-package check. It uses no model, network, account
// data or WeChat send; the only child is ffmpeg -version in the same LPAC path
// exposed to a trusted owner's normal project-plugin call.
func TestLivePackagedToolIndexRunsFFmpegInWindowsSandbox(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("WECHAT_PACKAGED_TOOL_TEST") != "1" {
		t.Skip("opt-in Windows packaged runtime verification")
	}
	f, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	python, err := filepath.Abs("../../dist/windows-native/runtimes/windows-python-cli/python.exe")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs("../../scripts/windows_agent_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	media, err := filepath.Abs("../../dist/windows-native/runtimes/windows-media/ffmpeg.exe")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{python, helper, media} {
		if info, e := os.Stat(path); e != nil || !info.Mode().IsRegular() {
			t.Fatalf("required packaged runtime missing: %s: %v", path, e)
		}
	}
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	cfg.Host.Windows = &agenthost.WindowsConfig{Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), Helper: helper, Spool: filepath.Join(f.dir, "packaged-tool-spool")}
	cfg.TasksEnabled = true
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}

	indexed, err := p.callProjectPlugin(t.Context(), "server_tools", json.RawMessage(`{"id":"media.ffmpeg"}`))
	if err != nil || !strings.Contains(indexed, `"available":true`) || !strings.Contains(indexed, `"id":"media.ffmpeg"`) {
		t.Fatalf("ffmpeg was not available from normal tool index: %s %v", indexed, err)
	}
	start, _ := json.Marshal(map[string]any{"action": "start", "path": p.executionContext().Conversation, "argv": []string{media, "-version"}, "request_key": "packaged-ffmpeg-version", "timeout_seconds": 30})
	result, err := p.callProjectPlugin(t.Context(), "server_windows_process", start)
	if err != nil {
		t.Fatal(err)
	}
	var process struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Output   string `json:"output"`
		ExitCode *int   `json:"exit_code"`
	}
	if json.Unmarshal([]byte(result), &process) != nil || process.ID == "" {
		t.Fatal("ffmpeg start did not return a durable process", result)
	}
	status, _ := json.Marshal(map[string]string{"action": "status", "id": process.ID})
	deadline := time.Now().Add(20 * time.Second)
	for process.State == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		result, err = p.callProjectPlugin(t.Context(), "server_windows_process", status)
		if err != nil || json.Unmarshal([]byte(result), &process) != nil {
			t.Fatal("ffmpeg status unavailable", result, err)
		}
	}
	if process.State != "completed" || process.ExitCode == nil || *process.ExitCode != 0 || !strings.Contains(strings.ToLower(process.Output), "ffmpeg version") {
		t.Fatal("packaged ffmpeg did not execute in LPAC", result)
	}
}
