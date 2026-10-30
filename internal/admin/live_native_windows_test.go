package admin

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/agenthost"
)

// Exercise the production tool/permission/task adapters and an actual LPAC CMD
// process in an isolated synthetic conversation, without WeChat or model calls.
func TestLiveNativeWindowsTool(t *testing.T) {
	path := os.Getenv("WECHAT_WINDOWS_TEST_CONFIG")
	if runtime.GOOS != "windows" || path == "" {
		t.Skip("opt-in native Windows LPAC transport")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var windows agenthost.WindowsConfig
	if json.Unmarshal(b, &windows) != nil {
		t.Fatal("invalid synthetic Windows test configuration")
	}
	f, p := trustedAgent(t)
	defer f.manager.processes.Close()
	c := f.manager.agent.config()
	c.Enabled, c.TasksEnabled, c.Host.Mode = true, true, "full"
	c.Host.Windows = &windows
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"action": "start", "path": c.Host.Roots[0],
		"command":     "chcp 65001 >nul & echo Windows 原生工具 $literal & exit /b 7",
		"request_key": "native-windows-synthetic", "timeout_seconds": 15,
	})
	result, err := p.Call(t.Context(), "server_windows_process", args)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID        string `json:"id"`
		Automatic bool   `json:"automatic_followup"`
	}
	if json.Unmarshal([]byte(result), &started) != nil || started.ID == "" || !started.Automatic {
		t.Fatal("missing process identity or completion task", result)
	}
	var job agenthost.Process
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		job, err = f.manager.processes.Status(p.processScope(), started.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if job.State != "failed" || job.ExitCode != 7 || !strings.Contains(job.Output, "Windows 原生工具 $literal") {
		t.Fatalf("unexpected native CMD outcome: %+v", job)
	}
	same, err := p.Call(t.Context(), "server_windows_process", args)
	if err != nil || !strings.Contains(same, started.ID) {
		t.Fatal("logical request was not deduplicated", same, err)
	}
	if len(f.manager.agent.list("test", "wxid_owner")) != 1 {
		t.Fatal("process completion must retain exactly one continuation task")
	}
	t.Log("native LPAC tool: Unicode, exit status, deduplication and continuation task verified")
}
