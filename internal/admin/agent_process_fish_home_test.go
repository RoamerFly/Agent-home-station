package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/agenthost"
)

func TestFishHomeProcessFollowupNeverUsesLegacyScopeAfterUnbind(t *testing.T) {
	f, p := trustedAgent(t)
	settings := f.manager.agent.config()
	settings.TasksEnabled = true
	settings.Host.Mode = "full"
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	f.login()

	// A fish-home scope cannot collide with the normal direct-conversation
	// namespace. Normal status/list calls consequently have no route to it.
	fishID := fishManage(t, f, "test", "bind_fish", 200)
	if fishID == "" {
		t.Fatal("missing fish identity")
	}
	legacyScope := p.processScope()
	fishScope := agentID(p.trace.Account, p.trace.Thread, p.trace.Sender, "fish_home", fishID)
	if fishScope == legacyScope {
		t.Fatal("fish-home process reused the normal conversation scope")
	}

	task := agentTask{
		ID:         "fish-home-process-watcher",
		ProcessID:  "fish-home-process",
		FishHomeID: fishID,
		Account:    p.trace.Account,
		Thread:     p.trace.Thread,
		Sender:     p.trace.Sender,
		State:      "waiting",
		Next:       time.Now().Add(-time.Second),
	}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	// Revoke after persistence to model a service restart/automatic completion
	// race. claimProcessTask must fail closed before it even looks in a legacy
	// process scope, so an old same-ID process is never disclosed.
	if cleared := fishManage(t, f, "test", "unbind_fish", 200); cleared != "" {
		t.Fatalf("unbind returned fish identity %q", cleared)
	}
	stored, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok {
		t.Fatal("stored fish task missing")
	}
	in, err := f.manager.claimProcessTask(stored, time.Now())
	if err != nil || in != nil {
		t.Fatalf("revoked fish task resumed: input=%#v err=%v", in, err)
	}
	stored, ok = f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || stored.State != "cancelled" || stored.LastResult == "" {
		t.Fatalf("revoked fish task not durably cancelled: %#v", stored)
	}
}

func TestFishHomeProcessStartPersistsBindingAndRevocationStopsIt(t *testing.T) {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("real fish-home process check requires bwrap isolation")
		}
	} else if runtime.GOOS != "windows" {
		t.Skip("real fish-home process check has Linux and Windows transports only")
	}
	f, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	settings := f.manager.agent.config()
	settings.TasksEnabled = true
	settings.Host.Mode = "full"
	if runtime.GOOS == "windows" {
		configPath := os.Getenv("WECHAT_WINDOWS_TEST_CONFIG")
		if configPath == "" {
			t.Skip("set WECHAT_WINDOWS_TEST_CONFIG for the real Windows LPAC transport")
		}
		data, readErr := os.ReadFile(configPath)
		if readErr != nil {
			t.Fatalf("read WECHAT_WINDOWS_TEST_CONFIG: %v", readErr)
		}
		var windows agenthost.WindowsConfig
		if json.Unmarshal(data, &windows) != nil || windows.Pythonw == "" || windows.Helper == "" || windows.Spool == "" {
			t.Fatal("invalid local Windows LPAC configuration")
		}
		settings.Host.Windows = &windows
	}
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	f.login()
	fishID := fishManage(t, f, "test", "bind_fish", 200)

	cfg, err := p.scopedHostConfig(json.RawMessage(`{"fish_home":true}`))
	if err != nil || len(cfg.Roots) != 1 {
		t.Fatalf("fish-home config unavailable: %#v %v", cfg, err)
	}
	startedPath := filepath.Join(cfg.Roots[0], "ready")
	toolName := "server_process"
	args := map[string]any{"action": "start", "fish_home": true, "path": cfg.Roots[0], "request_key": "fish-home-revocation"}
	if runtime.GOOS == "windows" {
		toolName = "server_windows_process"
		python := filepath.Join(filepath.Dir(settings.Host.Windows.Pythonw), "python.exe")
		args["argv"] = []string{python, "-c", `import pathlib,time; pathlib.Path("ready").write_text("started", encoding="utf-8"); time.sleep(30)`}
	} else {
		args["command"] = "printf started > ready; sleep 30; echo fish-home-should-not-leak"
	}
	raw, _ := json.Marshal(args)
	result, err := p.Call(context.Background(), toolName, raw)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID     string `json:"id"`
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(result), &started); err != nil || started.ID == "" || started.TaskID == "" {
		t.Fatalf("fish-home process did not start with a durable task: %s (%v)", result, err)
	}
	fishScope := agentID(p.trace.Account, p.trace.Thread, p.trace.Sender, "fish_home", fishID)
	defer func() { _, _ = f.manager.processes.Status(fishScope, started.ID, true) }()
	startDeadline := time.Now().Add(10 * time.Second)
	for {
		process, statusErr := f.manager.processes.Status(fishScope, started.ID, false)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if bytes, readErr := os.ReadFile(startedPath); readErr == nil && string(bytes) == "started" && process.State == "running" {
			break
		}
		if process.State != "running" || time.Now().After(startDeadline) {
			t.Fatalf("fish-home process never reached its started checkpoint: %+v", process)
		}
		time.Sleep(20 * time.Millisecond)
	}
	task, ok := f.manager.agent.lookupTask(started.TaskID, p.trace.Account, p.trace.Thread)
	if !ok || task.FishHomeID != fishID {
		t.Fatalf("fish-home id was not persisted on watcher: %#v", task)
	}
	// The default process namespace has no route to a fish-home process even
	// before revocation; it must not be able to expose its pending output.
	normalStatus, _ := json.Marshal(map[string]any{"action": "status", "id": started.ID})
	if _, err := p.Call(context.Background(), toolName, normalStatus); err == nil {
		t.Fatal("ordinary process status reached fish-home process")
	}

	if cleared := fishManage(t, f, "test", "unbind_fish", 200); cleared != "" {
		t.Fatalf("unbind returned fish identity %q", cleared)
	}
	// The process job's live allowed callback observes the revoked resolver and
	// cancels the real command; do not infer this merely from watcher state.
	deadline := time.Now().Add(5 * time.Second)
	var stoppedOutput, stoppedState, stoppedReason string
	for {
		process, statusErr := f.manager.processes.Status(fishScope, started.ID, false)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if process.State != "running" {
			stoppedOutput = process.Output
			stoppedState, stoppedReason = process.State, process.Reason
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fish-home process remained running after binding revocation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(stoppedOutput, "fish-home-should-not-leak") {
		t.Fatalf("revoked fish-home command reached its output: %q", stoppedOutput)
	}
	if stoppedState != "cancelled" || stoppedReason != "cancelled_or_permission_revoked" {
		t.Fatalf("binding revocation did not cancel the real process: state=%q reason=%q", stoppedState, stoppedReason)
	}
	task, ok = f.manager.agent.lookupTask(started.TaskID, p.trace.Account, p.trace.Thread)
	if !ok {
		t.Fatal("fish-home watcher disappeared before revocation audit")
	}
	in, err := f.manager.claimProcessTask(task, time.Now())
	if err != nil || in != nil {
		t.Fatalf("revoked fish watcher disclosed a completion: input=%#v err=%v", in, err)
	}
	task, ok = f.manager.agent.lookupTask(started.TaskID, p.trace.Account, p.trace.Thread)
	if !ok || task.State != "cancelled" || task.LastResult == "" || task.LastResult == "fish-home-should-not-leak" {
		t.Fatalf("revoked watcher leaked or was not settled: %#v", task)
	}
}
