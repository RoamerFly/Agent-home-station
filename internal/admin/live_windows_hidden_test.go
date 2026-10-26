package admin

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/agenthost"
)

// Opt-in: production tool adapter with a synthetic conversation and REAL
// Windows child process. No real WeChat/model/network call is made.
func TestLiveHiddenWindowsTool(t *testing.T) {
	path := os.Getenv("WECHAT_WINDOWS_TEST_CONFIG")
	if path == "" {
		t.Skip("opt-in local Windows transport")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var windows agenthost.WindowsConfig
	if json.Unmarshal(b, &windows) != nil {
		t.Fatal("invalid local test configuration")
	}
	f, p := trustedAgent(t)
	defer f.manager.processes.Close()
	c := f.manager.agent.config()
	c.Host.Mode = "full"
	c.Host.Roots = []string{"/"}
	c.Host.Windows = &windows
	if e = f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	args, _ := json.Marshal(map[string]any{"action": "start", "path": `E:\A-Importance\wechat-bot`, "command": "[Console]::WriteLine('隐藏通道 $literal 中文'); exit 7", "request_key": "hidden-real-test", "timeout_seconds": 15})
	result, e := p.Call(t.Context(), "server_windows_process", args)
	if e != nil {
		t.Fatal(e)
	}
	var response struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(result), &response)
	if response.ID == "" {
		t.Fatal(result)
	}
	scope := agentID("test", "wxid_owner", "wxid_owner")
	deadline := time.Now().Add(20 * time.Second)
	var job agenthost.Process
	for time.Now().Before(deadline) {
		job, e = f.manager.processes.Status(scope, response.ID, false)
		if e != nil {
			t.Fatal(e)
		}
		if job.State != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if job.State != "failed" || job.ExitCode != 7 || !strings.Contains(job.Output, "隐藏通道 $literal 中文") {
		t.Fatal(job)
	}
	same, e := p.Call(t.Context(), "server_windows_process", args)
	if e != nil || !strings.Contains(same, response.ID) {
		t.Fatal("duplicate logical request re-executed", same, e)
	}
	t.Logf("real Windows job=%s state=%s exit=%d Unicode verified", response.ID, job.State, job.ExitCode)
}
