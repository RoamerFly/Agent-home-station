package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginruntime"
)

// fishHomeCall is deliberately routed through the same installed project-tool
// integration as an Agent turn.  It does not reach into fish-home internals.
func fishHomeCall(t *testing.T, p *permittedTools, name string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Call(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("%s rejected: %v", name, err)
	}
	return out
}

func fishHomeDenied(t *testing.T, p *permittedTools, name string, args map[string]any) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := p.Call(context.Background(), name, raw); err == nil {
		t.Fatalf("%s unexpectedly admitted: %s", name, out)
	}
}

func fishHomeRoots(t *testing.T, p *permittedTools) []string {
	t.Helper()
	out := fishHomeCall(t, p, "server_status", map[string]any{"fish_home": true})
	var status struct {
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil || len(status.Roots) != 1 || !strings.Contains(filepath.ToSlash(status.Roots[0]), "/fish_home/fish-") || !strings.Contains(filepath.ToSlash(status.Roots[0]), "/private/") {
		t.Fatalf("fish home did not expose exactly one durable private mount: %s (%v)", out, err)
	}
	return status.Roots
}

// restartFishHomeRuntime reopens the already-installed package rather than
// reinstalling it: installation is intentionally rejected while enabled.
func restartFishHomeRuntime(t *testing.T, p *permittedTools) {
	t.Helper()
	store, err := p.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	controller, err := pluginruntime.NewController(store, &projectSyntheticExecutor{p: p})
	if err != nil {
		t.Fatal(err)
	}
	p.manager.packageMu.Lock()
	p.manager.packageRuntime, p.manager.packageRuntimeReady = controller, true
	p.manager.packageMu.Unlock()
	t.Cleanup(func() { _ = controller.Close() })
}

func TestFishHomeOwnerBoundAccountsPersistAndDenyOtherScopes(t *testing.T) {
	f, accounts := managedAccountPair(t)
	pair := accounts
	for _, p := range pair {
		installProjectPluginTestRuntime(t, p)
		p.SetTrace(companion.Input{Channel: "wechat", Account: p.manager.native.Account(), Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "fish-home-owner-" + p.manager.native.Account(), Mode: "direct"})
	}

	// Binding is a normal authenticated administration action; the tool request
	// still has to opt in explicitly with fish_home:true.
	fishID := fishManage(t, f, "test", "bind_fish", 200)
	if other := fishManage(t, f, "second", "bind_fish", 200); other != fishID {
		t.Fatalf("two bound accounts received different fish identities: %q != %q", other, fishID)
	}
	normal := fishHomeCall(t, pair[0], "server_status", map[string]any{})
	if strings.Contains(filepath.ToSlash(normal), "/fish_home/") {
		t.Fatalf("ordinary private conversation silently entered fish home: %s", normal)
	}

	roots := fishHomeRoots(t, pair[0])
	otherRoots := fishHomeRoots(t, pair[1])
	if roots[0] == otherRoots[0] || filepath.Dir(filepath.Dir(roots[0])) != filepath.Dir(filepath.Dir(otherRoots[0])) {
		t.Fatalf("fish home must share only the fish storage parent, never account diaries: %q != %q", roots[0], otherRoots[0])
	}
	tool := filepath.Join(roots[0], "self-authored-tool.py")
	fishHomeCall(t, pair[0], "server_write", map[string]any{"fish_home": true, "path": tool, "content": "print('fish-home-v1')\n"})
	read := fishHomeCall(t, pair[0], "server_read", map[string]any{"fish_home": true, "path": tool, "length": 4096})
	var version struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(read), &version); err != nil || version.SHA256 == "" || !strings.Contains(read, "fish-home-v1") {
		t.Fatalf("fish-home self-authored tool unreadable: %s (%v)", read, err)
	}
	fishHomeCall(t, pair[0], "server_edit", map[string]any{"fish_home": true, "path": tool, "expected_sha256": version.SHA256, "old_text": "fish-home-v1", "new_text": "fish-home-v2"})
	fishHomeDenied(t, pair[1], "server_read", map[string]any{"fish_home": true, "path": tool, "length": 4096})
	if got := fishHomeCall(t, pair[1], "server_list", map[string]any{"fish_home": true, "path": otherRoots[0], "limit": 50}); strings.Contains(got, "self-authored-tool.py") {
		t.Fatalf("second account could list the first account's private fish-home diary: %s", got)
	}
	// Revocation is evaluated at invocation time, not only when the policy was
	// first constructed. A formerly bound account immediately loses the mount.
	pair[1].SetTrace(companion.Input{Channel: "wechat", Account: "second", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "fish-home-second-revoked", Mode: "direct"})
	fishManage(t, f, "second", "unbind_fish", 200)
	fishHomeDenied(t, pair[1], "server_read", map[string]any{"fish_home": true, "path": tool, "length": 4096})

	// The path is durable across a real Manager restart, and reopening via the
	// normal tool catalog still finds the self-authored tool without retaining
	// an old in-memory root or policy.
	if _, err := f.manager.Save(ConfigInput{Config: f.manager.Config().Config}); err != nil {
		t.Fatalf("persist synthetic recipient policy before restart: %v", err)
	}
	f = restartFishFixture(t, f)
	if rows := fishAccountRows(t, f); rows["test"] != fishID || rows["second"] != "" {
		t.Fatalf("restart did not retain the remaining fish binding: %#v", rows)
	}
	restarted, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	restarted.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "fish-home-after-restart", Mode: "direct"})
	restartFishHomeRuntime(t, restarted)
	if after := fishHomeRoots(t, restarted); after[0] != roots[0] {
		t.Fatalf("fish home path changed across manager restart: %q != %q", after[0], roots[0])
	}
	if got := fishHomeCall(t, restarted, "server_read", map[string]any{"fish_home": true, "path": tool, "length": 4096}); !strings.Contains(got, "fish-home-v2") {
		t.Fatalf("restart lost self-authored fish-home tool: %s", got)
	}
	pair[0] = restarted
	// fish_home is incompatible with an account-wide workspace elevation.
	fishHomeDenied(t, pair[0], "server_status", map[string]any{"fish_home": true, "global": true})

	// A group owner must not turn the group into a shared shell; neither can an
	// ordinary member. Check the execution endpoint as well as file APIs.
	deniedToolArgs := func(process bool) map[string]any {
		if process {
			if runtime.GOOS == "windows" {
				return map[string]any{"fish_home": true, "action": "start", "path": roots[0], "command": "echo forbidden", "request_key": "fish-home-denied"}
			}
			return map[string]any{"fish_home": true, "path": roots[0], "command": "echo forbidden"}
		}
		return map[string]any{"fish_home": true, "path": tool, "length": 4096}
	}
	execTool := "server_exec"
	if runtime.GOOS == "windows" {
		execTool = "server_windows_process"
	}
	group := companion.Input{Channel: "wechat", Account: "test", Thread: "same@chatroom", Sender: "wxid_owner", TraceID: "fish-home-group-owner", Mode: "direct", IsGroup: true}
	pair[0].SetTrace(group)
	fishHomeDenied(t, pair[0], "server_status", map[string]any{"fish_home": true})
	fishHomeDenied(t, pair[0], "server_read", deniedToolArgs(false))
	fishHomeDenied(t, pair[0], execTool, deniedToolArgs(true))
	member := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_member", TraceID: "fish-home-member", Mode: "direct"}
	pair[0].SetTrace(member)
	fishHomeDenied(t, pair[0], "server_status", map[string]any{"fish_home": true})
	fishHomeDenied(t, pair[0], "server_read", deniedToolArgs(false))
	fishHomeDenied(t, pair[0], execTool, deniedToolArgs(true))

	// The other active binding remains usable.
	pair[0].SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "fish-home-first-still-bound", Mode: "direct"})
	if got := fishHomeCall(t, pair[0], "server_read", map[string]any{"fish_home": true, "path": tool, "length": 4096}); !strings.Contains(got, "fish-home-v2") {
		t.Fatalf("remaining binding lost home after another account unbound: %s", got)
	}
	fishManage(t, f, "test", "unbind_fish", 200)
	fishHomeDenied(t, pair[0], "server_read", map[string]any{"fish_home": true, "path": tool, "length": 4096})
}

// Opt-in real LPAC check: only synthetic code in the authenticated owner's
// fish-private mount is written and run; no WeChat/model/network action occurs.
func TestFishHomeRealWindowsLPACSelfAuthoredTool(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows LPAC")
	}
	configPath := os.Getenv("WECHAT_WINDOWS_TEST_CONFIG")
	if configPath == "" {
		t.Skip("opt-in Windows LPAC configuration not provided")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var windows agenthost.WindowsConfig
	if err = json.Unmarshal(data, &windows); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(filepath.Dir(windows.Pythonw), "python.exe")
	if _, err = os.Stat(python); err != nil {
		t.Skipf("LPAC Python CLI unavailable: %v", err)
	}
	// The dedicated revocation test exercises a long working path.
	shortRoot, err := os.MkdirTemp("", "fish-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(shortRoot); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("WECHAT_WORKSPACE_ROOT", shortRoot)
	f, p := trustedAgent(t)
	f.login()
	if id := fishManage(t, f, "test", "bind_fish", 200); id == "" {
		t.Fatal("fish binding missing")
	}
	cfg := f.manager.agent.config()
	cfg.Host.Mode, cfg.Host.Windows = "full", &windows
	if err = f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	// The project package has a broker helper distinct from the LPAC agent-host
	// helper in the supplied config. Use the production Controller so this is a
	// full package-to-host exercise, not just a policy simulation.
	store := installProjectPluginFixture(t, p)
	pluginHelper := filepath.Join(filepath.Dir(windows.Helper), "windows_plugin_host.pyw")
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: windows.Pythonw, WindowsHelper: pluginHelper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	t.Cleanup(func() { _ = controller.Close() })
	status := fishHomeCall(t, p, "server_status", map[string]any{"fish_home": true})
	var mount struct {
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal([]byte(status), &mount); err != nil || len(mount.Roots) != 1 || !filepath.IsAbs(mount.Roots[0]) {
		t.Fatalf("real fish-home status mount unavailable: %s (%v)", status, err)
	}
	tool := filepath.Join(mount.Roots[0], "real_fish_tool.py")
	fishHomeCall(t, p, "server_write", map[string]any{"fish_home": true, "path": tool, "content": "print('real-fish-home-lpac-ok')\n"})
	read := fishHomeCall(t, p, "server_read", map[string]any{"fish_home": true, "path": tool, "length": 4096})
	var file struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(read), &file); err != nil || file.SHA256 == "" || !strings.Contains(read, "lpac-ok") {
		t.Fatalf("real fish script unreadable: %s", read)
	}
	fishHomeCall(t, p, "server_edit", map[string]any{"fish_home": true, "path": tool, "expected_sha256": file.SHA256, "old_text": "lpac-ok", "new_text": "lpac-edited-ok"})
	// Reopen the complete manager and package controller; no prior mount or
	// process object is reused. The durable fish path and edited script must be
	// rediscovered through the public status/read/process tools.
	persisted := f.manager.Config().Config
	persisted.GroupRules = append(persisted.GroupRules, GroupRule{ID: "fish-canary@chatroom", Mode: "mention"})
	if _, err = f.manager.Save(ConfigInput{Config: persisted}); err != nil {
		t.Fatal(err)
	}
	f = restartFishFixture(t, f)
	p, _, err = f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "real-fish-after-restart", Mode: "direct"})
	store, err = f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	controller, err = pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: windows.Pythonw, WindowsHelper: pluginHelper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	t.Cleanup(func() { _ = controller.Close() })
	status = fishHomeCall(t, p, "server_status", map[string]any{"fish_home": true})
	if err = json.Unmarshal([]byte(status), &mount); err != nil || len(mount.Roots) != 1 || filepath.Join(mount.Roots[0], "real_fish_tool.py") != tool {
		t.Fatalf("fish home changed after restart: %s", status)
	}
	if got := fishHomeCall(t, p, "server_read", map[string]any{"fish_home": true, "path": tool, "length": 4096}); !strings.Contains(got, "lpac-edited-ok") {
		t.Fatalf("edited script absent after restart: %s", got)
	}

	// Execute only after restart to prove the stored revision is reusable.
	start := fishHomeCall(t, p, "server_windows_process", map[string]any{"fish_home": true, "action": "start", "path": mount.Roots[0], "argv": []string{python, "real_fish_tool.py"}, "request_key": "real-fish-home-lpac-tool", "timeout_seconds": 20})
	var process struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Output   string `json:"output"`
		ExitCode *int   `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(start), &process); err != nil || process.ID == "" {
		t.Fatalf("LPAC tool did not start: %s (%v)", start, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for process.State == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		result := fishHomeCall(t, p, "server_windows_process", map[string]any{"fish_home": true, "action": "status", "id": process.ID})
		if err := json.Unmarshal([]byte(result), &process); err != nil {
			t.Fatalf("invalid LPAC status: %s (%v)", result, err)
		}
	}
	if process.State != "completed" || process.ExitCode == nil || *process.ExitCode != 0 || !strings.Contains(process.Output, "real-fish-home-lpac-edited-ok") {
		t.Fatalf("real self-authored LPAC fish tool failed: %+v", process)
	}
	// An ordinary group member receives a normal group workspace, never the
	// fish mount. The real LPAC child must be unable to open an already-known
	// absolute private diary path; only PermissionError is a successful canary.
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "fish-canary@chatroom", Sender: "wxid_member", TraceID: "real-fish-group-canary", Mode: "direct", IsGroup: true})
	normalStatus := fishHomeCall(t, p, "server_status", map[string]any{})
	var normalMount struct {
		Roots []string `json:"roots"`
	}
	if err = json.Unmarshal([]byte(normalStatus), &normalMount); err != nil || len(normalMount.Roots) == 0 || normalMount.Roots[0] == mount.Roots[0] {
		t.Fatalf("group did not receive an ordinary isolated root: %s", normalStatus)
	}
	canary := filepath.Join(normalMount.Roots[0], "known_fish_path_canary.py")
	canaryCode := "from pathlib import Path\ntry:\n Path(" + strconv.Quote(tool) + ").read_text(encoding='utf-8')\nexcept PermissionError:\n print('fish-private-path-denied')\nelse:\n raise SystemExit('fish private path was readable')\n"
	fishHomeCall(t, p, "server_write", map[string]any{"path": canary, "content": canaryCode})
	canaryStart := fishHomeCall(t, p, "server_windows_process", map[string]any{"action": "start", "path": normalMount.Roots[0], "argv": []string{python, "known_fish_path_canary.py"}, "request_key": "real-fish-group-known-path-canary", "timeout_seconds": 20})
	if err = json.Unmarshal([]byte(canaryStart), &process); err != nil || process.ID == "" {
		t.Fatalf("group canary did not start: %s", canaryStart)
	}
	deadline = time.Now().Add(30 * time.Second)
	for process.State == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		result := fishHomeCall(t, p, "server_windows_process", map[string]any{"action": "status", "id": process.ID})
		if err = json.Unmarshal([]byte(result), &process); err != nil {
			t.Fatalf("invalid group canary status: %s", result)
		}
	}
	if process.State != "completed" || process.ExitCode == nil || *process.ExitCode != 0 || !strings.Contains(process.Output, "fish-private-path-denied") {
		t.Fatalf("group LPAC read fish private path: %+v", process)
	}
}
