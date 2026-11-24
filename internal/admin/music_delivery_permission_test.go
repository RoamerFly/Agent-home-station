package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mediaspec"
)

func TestMusicJobStatusContinuesWhileDeliveryPermissionIsClosed(t *testing.T) {
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/jobs/original-job" {
			writes.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"original-job","state":"completed","duration_seconds":180}`))
	}))
	defer server.Close()
	f := setup(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendVoice = false
	f.manager.config.Companion.Permissions.SendFile = false
	f.manager.mu.Unlock()
	speech := mediaspec.Defaults()
	speech.YuEEnabled = true
	speech.YuEEndpoint = server.URL
	speech.YuEToken = "synthetic-local-private-token"
	writeLegacySpeechMigrationFixture(t, f.manager, speech)
	installLegacyLocalMusicPlugin(t, f.manager)
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "original-request"})
	task := agentTask{ID: "original-song-task", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, OriginTrace: p.trace.TraceID, MusicJob: "original-job", MusicKind: "voice", State: "waiting", MusicDeadline: time.Now().Add(time.Hour), Constraints: p.taskConstraints("music", "", "", "")}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	input, err := f.manager.claimAgentTask(now, func(string) bool { return false })
	if err != nil || input != nil || reads.Load() != 1 || writes.Load() != 0 {
		stored, _ := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
		t.Fatalf("read-only status check unexpectedly delivered or submitted: input=%v err=%v reads=%d writes=%d stored=%+v plugin_ready=%v", input, err, reads.Load(), writes.Load(), stored, f.manager.localMusicPluginConfigured(t.Context()))
	}
	blocked, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || !blocked.MusicDeliveryBlocked || !blocked.MusicStatusTerminal || blocked.State != "waiting" || blocked.MusicJob != task.MusicJob || !strings.Contains(blocked.LastResult, "已完成") || !strings.Contains(blocked.LastResult, "等待恢复") {
		t.Fatalf("completion and permission recovery were not saved: %+v", blocked)
	}
	input, err = f.manager.claimAgentTask(now.Add(2*time.Minute), func(string) bool { return false })
	if err != nil || input != nil || reads.Load() != 1 {
		t.Fatalf("terminal job was polled again while delivery was blocked: input=%v err=%v reads=%d", input, err, reads.Load())
	}
	readOnly := p.yueTools()
	if len(readOnly) != 2 {
		t.Fatalf("status tools should remain available without delivery permission: %+v", readOnly)
	}
	for _, tool := range readOnly {
		if tool.Name != "music_local_status" && tool.Name != "music_service_status" {
			t.Fatalf("effectful music tool remained available: %s", tool.Name)
		}
	}
	p.trace.TaskID = task.ID
	result, err := p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"original-job","kind":"voice"}`))
	var metadata struct {
		DeliveryBlocked bool `json:"delivery_blocked"`
	}
	if err != nil || json.Unmarshal([]byte(result), &metadata) != nil || !metadata.DeliveryBlocked || reads.Load() != 2 || writes.Load() != 0 {
		t.Fatalf("manual status read attempted delivery: result=%s err=%v reads=%d writes=%d", result, err, reads.Load(), writes.Load())
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVoice = true
	f.manager.mu.Unlock()
	input, err = f.manager.claimAgentTask(now, func(string) bool { return false })
	if err != nil || input == nil || input.TaskID != task.ID || reads.Load() != 3 || writes.Load() != 0 {
		t.Fatalf("permission restoration did not resume the original job immediately: input=%v err=%v reads=%d writes=%d", input, err, reads.Load(), writes.Load())
	}
	resumed, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || resumed.MusicDeliveryBlocked || resumed.State != "running" || resumed.MusicJob != task.MusicJob {
		t.Fatalf("resumed task lost its original job: %+v", resumed)
	}
}
