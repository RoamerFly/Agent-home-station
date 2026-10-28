package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// Two real managers, files and journals with a synthetic native HTTP server.
// This exercises account composition, not two real logged-in WeChat accounts.
func managedAccountPair(t *testing.T) (*webFixture, [2]*permittedTools) {
	t.Helper()
	t.Setenv("WECHAT_WORKSPACE_ROOT", "")
	f, registry := setupAccounts(t)
	var tools [2]*permittedTools
	for i, m := range []*Manager{f.manager, registry.lookup("second").manager} {
		m.mu.Lock()
		m.config.PrivateEnabled = true
		m.config.DirectIDs = []string{"wxid_owner"}
		m.config.GroupRules = []GroupRule{{ID: "same@chatroom", Mode: "mention"}, {ID: "other@chatroom", Mode: "mention"}}
		m.config.Companion.Permissions.Remember = true
		m.mu.Unlock()
		c := m.agent.config()
		c.Enabled = true
		c.TasksEnabled = true
		c.CrossConversation = true
		c.OwnerIDs = []string{"wxid_owner"}
		c.PrivilegedThreads = []string{"*"}
		c.Host.Mode = "full"
		c.Host.Roots = []string{filepath.VolumeName(f.dir) + string(os.PathSeparator)}
		if err := m.agent.save(c); err != nil {
			t.Fatal(err)
		}
		p, _, err := m.toolAccess()
		if err != nil {
			t.Fatal(err)
		}
		p.SetTrace(companion.Input{Channel: "wechat", Account: m.native.Account(), Thread: "same@chatroom", Sender: "wxid_owner", TraceID: "same-request", Mode: "direct", IsGroup: true})
		if _, err = p.hostConfig(); err != nil {
			t.Fatal(err)
		}
		tools[i] = p
	}
	return f, tools
}

func accountTool(t *testing.T, p *permittedTools, name string, args map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := p.Call(t.Context(), name, raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}
func deniedAccountTool(t *testing.T, p *permittedTools, name string, args map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(args)
	if _, err := p.Call(t.Context(), name, raw); err == nil {
		t.Fatalf("%s unexpectedly admitted", name)
	}
}

func TestManagedAccountPairFilesMemoryTasksAndCapabilityProjection(t *testing.T) {
	f, p := managedAccountPair(t)
	x := [2]ExecutionContext{p[0].executionContext(), p[1].executionContext()}
	if x[0].Private == x[1].Private || x[0].Conversation == x[1].Conversation || x[0].Public != x[1].Public {
		t.Fatal("incorrect account/public layout")
	}
	var wg sync.WaitGroup
	failures := make(chan error, len(p))
	for i := range p {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for _, action := range []struct {
				name string
				args map[string]any
			}{
				{"server_write", map[string]any{"path": filepath.Join(x[i].Conversation, "scratch", "same.txt"), "content": "account-" + x[i].Account}},
				{"agent_task", map[string]any{"key": "same-key", "title": "task-" + x[i].Account, "checkpoint": "synthetic check", "state": "waiting", "delay_seconds": 3600}},
			} {
				raw, _ := json.Marshal(action.args)
				if _, err := p[i].Call(t.Context(), action.name, raw); err != nil {
					failures <- fmt.Errorf("%s: %w", action.name, err)
					return
				}
			}
			in := p[i].trace
			in.Text = "memory-" + x[i].Account
			if err := p[i].manager.memory.Observe(in, p[i].manager.config.Companion, time.Now()); err != nil {
				failures <- err
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	for i := range p {
		other := 1 - i
		got := accountTool(t, p[i], "server_read", map[string]any{"path": filepath.Join(x[i].Conversation, "scratch", "same.txt")})
		if !strings.Contains(got, "account-"+x[i].Account) {
			t.Fatal("own file missing", got)
		}
		deniedAccountTool(t, p[i], "server_read", map[string]any{"path": filepath.Join(x[other].Conversation, "scratch", "same.txt"), "global": true})
		got = accountTool(t, p[i], "agent_global_search", map[string]any{"query": "memory-"})
		if !strings.Contains(got, "memory-"+x[i].Account) || strings.Contains(got, "memory-"+x[other].Account) {
			t.Fatal("account memory mixed", got)
		}
		tasks := p[i].manager.agent.list(x[i].Account, "same@chatroom")
		if len(tasks) != 1 || tasks[0].Title != "task-"+x[i].Account {
			t.Fatal("account task journal mixed", tasks)
		}
	}
	shared := filepath.Join(x[0].Public, "projects", "shared-marker.md")
	accountTool(t, p[0], "server_write", map[string]any{"path": shared, "content": "shared public program"})
	// Owner can explicitly inspect another session of its own account.
	otherTrace := p[0].trace
	otherTrace.Thread = "other@chatroom"
	p[0].SetTrace(otherTrace)
	ownOther := p[0].executionContext()
	if _, err := p[0].hostConfig(); err != nil {
		t.Fatal(err)
	}
	accountTool(t, p[0], "server_write", map[string]any{"path": filepath.Join(ownOther.Conversation, "scratch", "other.txt"), "content": "same account other session"})
	otherTrace.Thread = "same@chatroom"
	p[0].SetTrace(otherTrace)
	deniedAccountTool(t, p[0], "server_read", map[string]any{"path": filepath.Join(ownOther.Conversation, "scratch", "other.txt")})
	accountTool(t, p[0], "server_read", map[string]any{"path": filepath.Join(ownOther.Conversation, "scratch", "other.txt"), "global": true})
	// A following member request inherits neither owner nor global access.
	member := p[0].trace
	member.Sender = "wxid_member"
	p[0].SetTrace(member)
	deniedAccountTool(t, p[0], "server_read", map[string]any{"path": filepath.Join(ownOther.Conversation, "scratch", "other.txt"), "global": true})
	deniedAccountTool(t, p[0], "agent_global_search", map[string]any{})
	deniedAccountTool(t, p[0], "server_write", map[string]any{"path": filepath.Join(x[0].Public, "projects", "member-change.md"), "content": "denied"})
	// Public code is usable by a member of the second account, not writable.
	member = p[1].trace
	member.Sender = "wxid_member"
	p[1].SetTrace(member)
	if !strings.Contains(accountTool(t, p[1], "server_read", map[string]any{"path": shared}), "shared public program") {
		t.Fatal("shared program not available")
	}
	deniedAccountTool(t, p[1], "server_read", map[string]any{"path": filepath.Join(x[0].Conversation, "scratch", "same.txt")})
	// Console projection reports effective roots per account, preserving config.
	for i, tool := range p {
		code, body, _ := f.request("GET", "/api/agent/capabilities?owner=wxid_owner&account="+x[i].Account, nil, true, false)
		var report struct {
			Host struct {
				Roots     []string `json:"roots"`
				Isolation any      `json:"isolation"`
			}
			Workspace ExecutionContext `json:"workspace"`
			Identity  string           `json:"identity"`
			Required  bool             `json:"isolation_required"`
		}
		if code != 200 || json.Unmarshal(body, &report) != nil {
			t.Fatal(code, string(body))
		}
		expected := tool.executionContext()
		ownerPath := filepath.Join(expected.Private, "conversations", companion.ScopeID("wechat", x[i].Account, "wxid_owner"))
		if !report.Required || report.Host.Isolation == nil || len(report.Host.Roots) != 2 || report.Host.Roots[0] != ownerPath || report.Host.Roots[1] != expected.Public || report.Workspace.Account != x[i].Account || strings.Contains(report.Identity, "Windows interop") {
			t.Fatal("legacy host projection", report)
		}
		if tool.manager.agent.config().Host.Roots[0] != filepath.VolumeName(f.dir)+string(os.PathSeparator) {
			t.Fatal("inspection changed stored policy")
		}
	}
}

func TestManagedAccountPairRealLinuxCommandsAndReadOnlyPublicProjects(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real Bubblewrap integration; file and journal checks run on all platforms")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not installed")
	}
	_, p := managedAccountPair(t)
	x := [2]ExecutionContext{p[0].executionContext(), p[1].executionContext()}
	shared := filepath.Join(x[0].Public, "projects", "shared-program.txt")
	accountTool(t, p[0], "server_write", map[string]any{"path": shared, "content": "synthetic shared program"})
	for i := range p {
		other := 1 - i
		trace := p[i].trace
		trace.Sender = "wxid_member"
		p[i].SetTrace(trace)
		// The child must be unable to see the other account or modify public code.
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
		command := fmt.Sprintf("set -e; test ! -e %s; test ! -e /mnt/c; test ! -e /run/WSL; cat %s; if printf forbidden > %s; then exit 8; fi; /bin/sh -c 'printf isolated-child > scratch/child.txt'; printf verified", quote(x[other].Private), quote(shared), quote(shared))
		raw := accountTool(t, p[i], "server_exec", map[string]any{"path": x[i].Conversation, "command": command})
		var result struct {
			Exit   int    `json:"exit_code"`
			Output string `json:"output"`
		}
		if json.Unmarshal([]byte(raw), &result) != nil || result.Exit != 0 || !strings.Contains(result.Output, "verified") {
			t.Fatal("sandbox boundary failed", raw)
		}
		if data, err := os.ReadFile(filepath.Join(x[i].Conversation, "scratch", "child.txt")); err != nil || string(data) != "isolated-child" {
			t.Fatal("child output unavailable", err)
		}
	}
	if data, err := os.ReadFile(shared); err != nil || string(data) != "synthetic shared program" {
		t.Fatal("public project modified", err)
	}
}
