package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func trustedAgent(t *testing.T) (*webFixture, *permittedTools) {
	t.Helper()
	f := setup(t)
	f.manager.mu.Lock()
	f.manager.config.PrivateEnabled = true
	f.manager.config.DirectIDs = []string{"wxid_owner"}
	f.manager.mu.Unlock()
	c := f.manager.agent.config()
	c.OwnerIDs = []string{"wxid_owner"}
	c.PrivilegedThreads = []string{"wxid_owner"}
	c.Host.Mode = "files"
	c.Host.Roots = []string{t.TempDir()}
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "turn-1", Mode: "direct"})
	effective, e := p.hostConfig()
	if e != nil {
		t.Fatal(e)
	}
	c = f.manager.agent.config()
	c.Host.Roots = effective.Roots
	if e = f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	return f, p
}

type taskOriginNative struct {
	*fakeNative
	message linuxnative.Message
}

func (n *taskOriginNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	if id == n.message.ID {
		return n.message, nil
	}
	return linuxnative.Message{}, nil
}

type taskCorrectionNative struct {
	*fakeNative
	messages map[string]linuxnative.Message
}

func (n *taskCorrectionNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	return n.messages[id], nil
}

func TestAdministratorResumesOriginalGroupVideoTaskWithVerifiedCorrection(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.mu.Lock()
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "room@chatroom", Mode: "mention"})
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	settings := f.manager.agent.config()
	settings.OwnerIDs = []string{"owner"}
	settings.PrivilegedThreads = []string{"room@chatroom"}
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	f.manager.native = &taskCorrectionNative{fakeNative: f.native, messages: map[string]linuxnative.Message{
		"new-image":  {ID: "new-image", AccountID: "test", ThreadID: "room@chatroom", SenderID: "owner", Direction: "incoming", Type: 3},
		"correction": {ID: "correction", AccountID: "test", ThreadID: "room@chatroom", SenderID: "owner", Direction: "incoming", Type: 1, Body: "只用这张参考图"},
	}}
	oldReceipt := &bot.TaskDelivery{Trace: "old-text", State: "accepted", TextExpected: 3, TextAccepted: 3, Expected: 1, Accepted: 1}
	old := agentTask{
		ID: "group-video", Account: "test", Thread: "room@chatroom", Sender: "owner", OwnerRequested: true,
		OriginTrace: "original", SourceMessageIDs: []string{"original"}, RequestedText: "原始两段共 15 秒视频请求", Goal: "原始两段共 15 秒视频请求",
		Title: "两段视频", Checkpoint: "轮询 old-job-needle 并发送旧双人图", State: "needs_review", Delivery: oldReceipt,
		Actions: []taskAction{{Tool: "station_comfy", Result: "old-job-needle", At: time.Now().Add(-time.Minute)}},
	}
	if err := f.manager.agent.task(old); err != nil {
		t.Fatal(err)
	}
	status, body, _ := f.request("POST", "/api/agent/tasks/"+old.ID, map[string]any{
		"action": "resume", "continuation_note": "取消旧双人图作业，只用新上传的 mini 参考图重做两段共 15 秒视频，最终成片发原群",
		"source_message_ids": []string{"new-image", "correction"}, "required_media_kind": "video",
	}, true, true)
	if status != http.StatusOK {
		t.Fatalf("correction resume: %d %s", status, body)
	}
	got, ok := f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
	if !ok || got.State != "pending" || got.ID != old.ID || got.Goal != old.Goal || got.RequestedText != old.RequestedText ||
		!slices.Equal(got.SourceMessageIDs, []string{"original", "new-image", "correction"}) || got.Delivery != nil || got.CorrectionOldDeliveryTrace != oldReceipt.Trace ||
		got.WorkCompleted || got.CorrectionAt.IsZero() || strings.Contains(got.Checkpoint, "old-job-needle") ||
		!strings.Contains(got.Checkpoint, "新上传的 mini 参考图") || !strings.Contains(got.Checkpoint, "old-text") || len(got.Actions) != 1 ||
		got.CorrectionRequiredMediaKind != "video" || got.CorrectionReferenceImageID != "new-image" {
		t.Fatalf("original task or accepted receipt was lost: %+v", got)
	}
	input, err := f.manager.claimAgentTask(time.Now().Add(time.Second), func(string) bool { return false })
	if err != nil || input == nil || input.TaskID != old.ID || strings.Contains(input.Text, "old-job-needle") ||
		!strings.Contains(input.Text, "new-image") || !strings.Contains(input.Text, "correction") || !strings.Contains(input.Text, "old-text") {
		t.Fatalf("corrected task context reintroduced old work: %+v %v", input, err)
	}
	if err := f.manager.updateAgentDelivery(*input, *oldReceipt); err != nil {
		t.Fatal(err)
	}
	got, _ = f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
	if got.Delivery != nil || !slices.Contains(got.CorrectionTraces, input.TraceID) {
		t.Fatalf("old receipt entered correction generation: %+v", got)
	}
	got.State = "completed"
	if _, err := f.manager.agent.saveTask(got); err == nil {
		t.Fatal("old task history satisfied corrected video")
	}
	access := &permittedTools{manager: f.manager, trace: *input}
	access.rememberTaskAction("station_comfy", json.RawMessage(`{"action":"run","source_message_id":"new-image"}`), `{"job_id":"new-job"}`, nil)
	beforePublish, _ := f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
	beforePublish.Delivery = &bot.TaskDelivery{Trace: input.TraceID, State: "accepted", Expected: 1, Accepted: 1, VideoExpected: 1, VideoAccepted: 1}
	if beforePublish.correctedDeliveryComplete() {
		t.Fatal("new Comfy job alone satisfied corrected video without a staged output")
	}
	access.rememberTaskAction("artifact_publish", json.RawMessage(`{"path":"new-final.mp4","task_id":"group-video"}`), `{"staged_only":true,"media":[{"kind":"video","id":"new-media"}]}`, nil)
	newReceipt := bot.TaskDelivery{Trace: input.TraceID, State: "accepted", Expected: 1, Accepted: 1, VideoExpected: 1, VideoAccepted: 1, VideoSourceID: "new-media"}
	if err := f.manager.updateAgentDelivery(*input, newReceipt); err != nil {
		t.Fatal(err)
	}
	got, _ = f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
	if got.CorrectionWorkStartedAt.IsZero() || got.CorrectionMediaPreparedAt.IsZero() || got.Delivery == nil || got.State != "waiting" || got.WorkCompleted {
		t.Fatalf("video receipt without completion intent should remain active: %+v", got)
	}
	got.State = "completed"
	if _, err := f.manager.agent.saveTask(got); err != nil {
		t.Fatalf("verified video should allow explicit completion: %v", err)
	}
	if err := f.manager.agent.retireSettled(); err != nil {
		t.Fatal(err)
	}
	if _, active := f.manager.agent.data.Tasks[old.ID]; active {
		t.Fatal("completed task was not archived")
	}
	status, body, _ = f.request("POST", "/api/agent/tasks/"+old.ID, map[string]any{
		"action": "reopen", "continuation_note": "再次只用这张参考图重新制作视频",
		"source_message_ids": []string{"new-image", "correction"}, "required_media_kind": "video",
	}, true, true)
	if status != http.StatusOK {
		t.Fatalf("archived correction reopen: %d %s", status, body)
	}
	got, ok = f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
	if !ok || got.State != "pending" || got.Delivery != nil || got.CorrectionOldDeliveryTrace != input.TraceID ||
		len(got.CorrectionTraces) != 0 || !got.CorrectionWorkStartedAt.IsZero() || !got.CorrectionMediaPreparedAt.IsZero() ||
		!strings.Contains(got.Checkpoint, "再次只用这张参考图") {
		t.Fatalf("archive reopen carried previous generation into new work: %+v", got)
	}
	previousCorrectionAt := got.CorrectionAt
	status, body, _ = f.request("POST", "/api/agent/tasks/"+old.ID, map[string]any{
		"action": "resume", "continuation_note": "第三次纠正，仍只使用这张参考图",
		"source_message_ids": []string{"new-image", "correction"}, "required_media_kind": "video",
	}, true, true)
	if status != http.StatusOK {
		t.Fatalf("active second correction: %d %s", status, body)
	}
	got, _ = f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
	if !got.CorrectionAt.After(previousCorrectionAt) || got.Delivery != nil || len(got.CorrectionTraces) != 0 ||
		!strings.Contains(got.Checkpoint, "第三次纠正") {
		t.Fatalf("second correction reused prior generation: %+v", got)
	}
}

func TestAdministratorCorrectionRejectsForeignMessagesWithoutChangingTask(t *testing.T) {
	f := setup(t)
	f.login()
	native := &taskCorrectionNative{fakeNative: f.native, messages: map[string]linuxnative.Message{}}
	f.manager.native = native
	old := agentTask{ID: "video", Account: "test", Thread: "room@chatroom", Sender: "owner", OriginTrace: "original", SourceMessageIDs: []string{"original"}, State: "needs_review", Checkpoint: "old checkpoint"}
	if err := f.manager.agent.task(old); err != nil {
		t.Fatal(err)
	}
	for name, message := range map[string]linuxnative.Message{
		"other-sender":  {ID: "candidate", AccountID: "test", ThreadID: "room@chatroom", SenderID: "other", Direction: "incoming"},
		"other-group":   {ID: "candidate", AccountID: "test", ThreadID: "other@chatroom", SenderID: "owner", Direction: "incoming"},
		"other-account": {ID: "candidate", AccountID: "other", ThreadID: "room@chatroom", SenderID: "owner", Direction: "incoming"},
		"outgoing":      {ID: "candidate", AccountID: "test", ThreadID: "room@chatroom", SenderID: "owner", Direction: "outgoing"},
	} {
		t.Run(name, func(t *testing.T) {
			native.messages["candidate"] = message
			status, _, _ := f.request("POST", "/api/agent/tasks/"+old.ID, map[string]any{
				"action": "resume", "continuation_note": "只用新图", "source_message_ids": []string{"candidate"},
			}, true, true)
			if status != http.StatusConflict {
				t.Fatalf("foreign correction accepted: %d", status)
			}
			got, ok := f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
			if !ok || got.State != old.State || got.Checkpoint != old.Checkpoint || !slices.Equal(got.SourceMessageIDs, old.SourceMessageIDs) {
				t.Fatalf("invalid correction changed task: %+v", got)
			}
		})
	}
}

func TestTaskCanBindVerifiedEarlierRequestAndReuseCompletedWork(t *testing.T) {
	f, p := trustedAgent(t)
	p.manager.native = &taskOriginNative{fakeNative: f.native, message: linuxnative.Message{
		ID: "song-request", AccountID: "test", ThreadID: "wxid_owner", SenderID: "wxid_owner",
		Direction: "incoming", Type: 1, Body: "请唱卜卦", ServerID: "original-server",
	}}
	late := "晚安"
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "later-message", OriginalText: &late, Mode: "direct"})
	p.recordCrossSourceMessages("wxid_owner", `{"items":[{"id":"song-request"}]}`)
	args := json.RawMessage(`{"key":"cover","title":"唱卜卦","checkpoint":"核对原成品","state":"waiting","origin_message_id":"song-request"}`)
	raw, err := p.Call(t.Context(), "agent_task", args)
	var first struct{ Task agentTask }
	if err != nil || json.Unmarshal([]byte(raw), &first) != nil || first.Task.OriginTrace != "song-request" || first.Task.RequestedText != "请唱卜卦" || first.Task.QuoteServerID != "original-server" || !slices.Contains(first.Task.SourceMessageIDs, "song-request") || first.Task.Constraints.Source != nil {
		t.Fatalf("earlier request binding failed: %s %v", raw, err)
	}
	raw, err = p.Call(t.Context(), "agent_tasks", json.RawMessage(`{"status":"active","query":"卜卦","limit":120}`))
	var active struct {
		Items          []agentTask `json:"items"`
		RequestedLimit int         `json:"requested_limit"`
		EffectiveLimit int         `json:"effective_limit"`
	}
	if err != nil || json.Unmarshal([]byte(raw), &active) != nil || len(active.Items) != 1 || active.Items[0].OriginTrace != "song-request" || active.RequestedLimit != 120 || active.EffectiveLimit != 100 {
		t.Fatalf("active task pagination ignored: %s %v", raw, err)
	}
	if err := f.manager.validateTaskSource(first.Task); err != nil {
		t.Fatalf("verified earlier request cannot continue: %v", err)
	}
	first.Task.State = "completed"
	if err := f.manager.agent.task(first.Task); err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "even-later-message", OriginalText: &late, Mode: "direct"})
	p.recordCrossSourceMessages("wxid_owner", `{"items":[{"id":"song-request"}]}`)
	raw, err = p.Call(t.Context(), "agent_task", args)
	var existing agentTask
	if err != nil || json.Unmarshal([]byte(raw), &existing) != nil || existing.ID != first.Task.ID || existing.State != "completed" || len(f.manager.agent.list("test", "wxid_owner")) != 1 {
		t.Fatalf("completed original was scheduled again: %s %v", raw, err)
	}
	raw, err = p.Call(t.Context(), "agent_tasks", json.RawMessage(`{"status":"history","query":"卜卦","limit":120}`))
	var history struct {
		Items []struct {
			Origin string `json:"origin_message_id"`
		} `json:"items"`
		RequestedLimit int `json:"requested_limit"`
		EffectiveLimit int `json:"effective_limit"`
	}
	if err != nil || json.Unmarshal([]byte(raw), &history) != nil || len(history.Items) != 1 || history.Items[0].Origin != "song-request" || history.RequestedLimit != 120 || history.EffectiveLimit != 100 {
		t.Fatalf("completed work not discoverable: %s %v", raw, err)
	}
}

func TestTaskContinuationUpdatesItsExistingCheckpoint(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.native = &taskOriginNative{fakeNative: f.native, message: linuxnative.Message{
		ID: "turn-1", AccountID: "test", ThreadID: "wxid_owner", SenderID: "wxid_owner",
		Direction: "incoming", Type: 1, Body: "请用柯南声线唱歌",
	}}
	first, err := p.Call(t.Context(), "agent_task", json.RawMessage(`{"key":"song","title":"柯南声线","checkpoint":"等待换声","state":"waiting"}`))
	var created struct{ Task agentTask }
	if err != nil || json.Unmarshal([]byte(first), &created) != nil || created.Task.ID == "" {
		t.Fatalf("initial task: %s %v", first, err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "task-retry", TaskID: created.Task.ID, OriginTraceID: "turn-1", BackgroundResult: true, Mode: "direct"})
	updated, err := p.Call(t.Context(), "agent_task", json.RawMessage(`{"key":"new-song-key","title":"konan声线","checkpoint":"按纠正后的声线继续","state":"waiting","origin_message_id":"turn-1"}`))
	var resumed struct{ Task agentTask }
	if err != nil || json.Unmarshal([]byte(updated), &resumed) != nil {
		t.Fatalf("continuation: %s %v", updated, err)
	}
	if resumed.Task.ID != created.Task.ID || resumed.Task.OriginTrace != created.Task.OriginTrace || resumed.Task.ParentID != "" || resumed.Task.Title != "konan声线" || len(f.manager.agent.list("test", "wxid_owner")) != 1 {
		t.Fatalf("continuation made another task: %+v", resumed.Task)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "later-correction", Mode: "direct"})
	p.recordCrossSourceMessages("wxid_owner", `{"items":[{"id":"turn-1"}]}`)
	corrected, err := p.Call(t.Context(), "agent_task", json.RawMessage(`{"key":"corrected-key","title":"konan原唱版","checkpoint":"按新的要求继续","state":"waiting","origin_message_id":"turn-1"}`))
	var linked struct{ Task agentTask }
	if err != nil || json.Unmarshal([]byte(corrected), &linked) != nil || linked.Task.ID != created.Task.ID || len(f.manager.agent.list("test", "wxid_owner")) != 1 {
		t.Fatalf("later correction duplicated the original task: %s %v", corrected, err)
	}
}

func TestOwnerCancellationArchivesSupersededTaskImmediately(t *testing.T) {
	f, p := trustedAgent(t)
	raw, err := p.Call(t.Context(), "agent_task", json.RawMessage(`{"key":"old-song","title":"旧歌曲续办","checkpoint":"等待旧作业","state":"waiting"}`))
	var created struct{ Task agentTask }
	if err != nil || json.Unmarshal([]byte(raw), &created) != nil || created.Task.ID == "" {
		t.Fatalf("create task: %s %v", raw, err)
	}
	f.login()
	code, _, _ := f.request(http.MethodPost, "/api/agent/tasks/"+created.Task.ID, map[string]string{"action": "cancel"}, true, true)
	if code != http.StatusOK {
		t.Fatalf("cancel status: %d", code)
	}
	if len(f.manager.agent.active("test", "wxid_owner")) != 0 {
		t.Fatal("cancelled task remained in the active list")
	}
	history := f.manager.agent.taskHistory("test", "wxid_owner")
	if len(history) != 1 || history[0].ID != created.Task.ID || history[0].State != "cancelled" {
		t.Fatalf("cancelled task not archived: %+v", history)
	}
}

func TestExistingOwnerConversationDiscoversStationComfy(t *testing.T) {
	_, p := trustedAgent(t)
	if !p.agentToolEnabled("station_comfy") {
		t.Fatal("owner conversation cannot discover the workstation ComfyUI bridge")
	}
	p.trace.Sender = "another-user"
	if p.agentToolEnabled("station_comfy") {
		t.Fatal("non-owner inherited the workstation ComfyUI bridge")
	}
}

func TestGroupMemberCanContinueExistingImageTask(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "room@chatroom", Mode: "mention"})
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.ProactiveThreads = nil
	f.manager.mu.Unlock()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "member", TraceID: "image-request", IsGroup: true, Mode: "direct"})
	if !p.agentToolEnabled("station_comfy") {
		t.Fatal("group image request cannot discover ComfyUI")
	}
	if _, err := p.Call(context.Background(), "agent_task", json.RawMessage(`{"key":"image","title":"居家照","checkpoint":"继续检查作品","state":"waiting","delay_seconds":0}`)); err != nil {
		t.Fatal(err)
	}
	tasks := f.manager.agent.active("test", "room@chatroom")
	if len(tasks) != 1 {
		t.Fatal("image task not persisted", tasks)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "owner", TraceID: "follow-up", IsGroup: true, Mode: "direct"})
	update := `{"id":"` + tasks[0].ID + `","checkpoint":"继续核对成品","state":"waiting","delay_seconds":0}`
	if _, err := p.Call(context.Background(), "agent_task", json.RawMessage(update)); err != nil {
		t.Fatal("later group message could not continue task", err)
	}
	stored, ok := f.manager.agent.lookupTask(tasks[0].ID, "test", "room@chatroom")
	if !ok || stored.Sender != "member" || stored.State != "waiting" {
		t.Fatal("task origin was replaced or lost", stored)
	}
	claimed, err := f.manager.claimAgentTask(time.Now().Add(time.Second), func(string) bool { return false })
	if err != nil || claimed == nil || claimed.TaskID != stored.ID || !claimed.Directed {
		t.Fatal("requested group task did not resume directly", claimed, err)
	}
}

func TestStationComfyArtifactStagesWechatImageWithoutSending(t *testing.T) {
	f, p := trustedAgent(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	f.manager.config.Companion.Permissions.SendImage = true
	f.manager.config.Companion.Permissions.Remember = true
	content := []byte("synthetic PNG bytes")
	hash := sha256.Sum256(content)
	id := "work-" + strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/local/artifacts/" + id:
			_ = json.NewEncoder(w).Encode(map[string]any{"artifact_id": id, "filename": "finished.png", "sha256": hex.EncodeToString(hash[:]), "bytes": len(content), "status": "registered"})
		case "/api/local/artifacts/" + id + "/download":
			_, _ = w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result, err := p.attachStationComfyArtifact(t.Context(), stationToolSession{BaseURL: server.URL}, id)
	if err != nil || !strings.Contains(result, `"kind":"image"`) || !strings.Contains(result, `"required_media_ids"`) {
		t.Fatalf("artifact was not staged for WeChat: %v %s", err, result)
	}
	if native.submits.Load() != 0 {
		t.Fatal("staging unexpectedly submitted a WeChat message")
	}
}

func TestMusicContinuationWithoutInstalledPluginNeedsReview(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.mu.Unlock()
	settings := f.manager.agent.config()
	settings.Enabled, settings.TasksEnabled = true, true
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	// A populated retired setting must not revive the legacy transport.
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEnabled = true
		legacySpeechFixture.YuEEndpoint = "http://127.0.0.1:9000"
		legacySpeechFixture.YuEToken = "synthetic-retired-token"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	task := agentTask{ID: "legacy-music-continuation", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "waiting", Next: time.Now().Add(-time.Second), MusicJob: "original-music-job"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
	if err != nil || in != nil {
		t.Fatal("uninstalled music plugin resumed legacy job", in, err)
	}
	stored := f.manager.agent.list(task.Account, task.Thread)
	if len(stored) != 1 || stored[0].State != "needs_review" || stored[0].MusicJob != task.MusicJob || stored[0].LastResult != "music_local_plugin_migration_required" {
		t.Fatal("legacy continuation was not retained for review", stored)
	}
}

func TestMusicContinuationWithUnreadyPluginNeedsReviewWithoutResubmission(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.mu.Unlock()
	settings := f.manager.agent.config()
	settings.Enabled, settings.TasksEnabled = true, true
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	// This official package is enabled for cloud generation but deliberately
	// lacks the local bridge connection needed to continue this existing job.
	installMusicPluginFixture(t, f, []string{"model.invoke"}, true)
	task := agentTask{ID: "unready-music-continuation", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "waiting", Next: time.Now().Add(-time.Second), MusicJob: "original-music-job"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
	if err != nil || in != nil {
		t.Fatal("unready music plugin submitted or resumed legacy job", in, err)
	}
	stored := f.manager.agent.list(task.Account, task.Thread)
	if len(stored) != 1 || stored[0].State != "needs_review" || stored[0].MusicJob != task.MusicJob || stored[0].LastResult != "music_local_plugin_configuration_required" {
		t.Fatal("unready continuation was not retained for review", stored)
	}
}
func TestAgentPrivilegeChecksAtDiscoveryAndExecution(t *testing.T) {
	_, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	if !p.agentToolEnabled("server_write") {
		t.Fatal("owner lacks tool")
	}
	b, _ := json.Marshal(map[string]string{"path": filepath.Join(p.manager.agent.config().Host.Roots[0], "owner.txt"), "content": "ok"})
	for _, in := range []companion.Input{{Account: "test", Thread: "wxid_owner", Sender: "wxid_attacker"}, {Account: "test", Thread: "filehelper", Sender: "wxid_owner"}, {Account: "other", Thread: "wxid_owner", Sender: "wxid_owner"}, {Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Preview: true}, {Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Surf: true}} {
		p.SetTrace(in)
		if p.agentToolEnabled("server_write") {
			t.Fatal("untrusted context offered host")
		}
		if _, err := p.Call(context.Background(), "server_write", b); err == nil {
			t.Fatal("untrusted context called host")
		}
	}
}

func TestOwnerAllAllowedThreadsStillChecksSenderAndRecipient(t *testing.T) {
	f, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	c := f.manager.agent.config()
	c.PrivilegedThreads = []string{"*"}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	// A newly allowed group does not need a second privileged-thread entry.
	f.manager.mu.Lock()
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "new_group@chatroom", Mode: "mention"})
	f.manager.mu.Unlock()
	for _, thread := range []string{"wxid_owner", "new_group@chatroom"} {
		p.SetTrace(companion.Input{Account: "test", Thread: thread, Sender: "wxid_owner"})
		if !p.agentToolEnabled("server_write") {
			t.Fatal("owner denied in allowed conversation", thread)
		}
	}
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(c.Host.Roots[0], "must-not-exist.txt"), "content": "forbidden"})
	for _, input := range []companion.Input{

		{Account: "test", Thread: "not_allowed@chatroom", Sender: "wxid_owner"},
		{Account: "test", Sender: "wxid_owner"},
		{Account: "other", Thread: "new_group@chatroom", Sender: "wxid_owner"},
		{Account: "test", Thread: "new_group@chatroom", Sender: "wxid_owner", Preview: true},
		{Account: "test", Thread: "new_group@chatroom", Sender: "wxid_owner", Surf: true},
	} {
		p.SetTrace(input)
		if p.agentToolEnabled("server_write") {
			t.Fatalf("untrusted context offered host: %+v", input)
		}
		if _, err := p.Call(context.Background(), "server_write", args); err == nil {
			t.Fatalf("untrusted context executed host: %+v", input)
		}
	}
	p.SetTrace(companion.Input{Account: "test", Thread: "new_group@chatroom", Sender: "wxid_member"})
	if !p.agentToolEnabled("server_write") {
		t.Fatal("member lacks isolated session file access")
	}
	if p.agentToolEnabled("wechat_conversations") || p.agentToolEnabled("agent_global_search") {
		t.Fatal("member acquired owner tools")
	}
	if _, err := p.Call(context.Background(), "server_write", args); err == nil {
		t.Fatal("member accessed account-wide directory")
	}
	if _, err := os.Stat(filepath.Join(c.Host.Roots[0], "must-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatal("unauthorized write reached filesystem", err)
	}
	for _, mutate := range []func(*agentSettings){
		func(s *agentSettings) { s.OwnerIDs = []string{"*"} },
		func(s *agentSettings) { s.OwnerIDs = nil; s.CrossConversation = true },
		func(s *agentSettings) { s.PrivilegedThreads = []string{"*", "wxid_owner"} },
	} {
		invalid := c
		mutate(&invalid)
		if invalid.validate() == nil {
			t.Fatal("invalid wildcard authorization accepted")
		}
	}
}

func TestExternalPluginOwnerRestrictionCannotBeBypassed(t *testing.T) {
	f, p := trustedAgent(t)
	installPublicWebFixture(t, f)
	c := f.manager.agent.config()
	c.ExternalOwnerOnly = true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	p.apiNames = map[string]bool{"voice_provider": true}
	if !p.enabled("voice_provider") || !p.enabled("external__tool") {
		t.Fatal("trusted plugin denied")
	}
	p.trace.Sender = "wxid_someone_else"
	if p.enabled("voice_provider") || p.enabled("external__tool") {
		t.Fatal("plugin bypassed owner restriction")
	}
	if !p.enabled("web_fetch") {
		t.Fatal("public web tool unnecessarily blocked")
	}
}
func TestAgentHostOperationDeduplicatedAcrossRestart(t *testing.T) {
	f, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	path := filepath.Join(f.manager.agent.config().Host.Roots[0], "once.txt")
	b, _ := json.Marshal(map[string]string{"path": path, "content": "once"})
	first, err := p.Call(context.Background(), "server_write", b)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Call(context.Background(), "server_write", b)
	if err != nil || first != second {
		t.Fatal("operation replay", first, second, err)
	}
	next, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.agent = next
	third, err := p.Call(context.Background(), "server_write", b)
	if err != nil || first != third {
		t.Fatal("restart replay", third, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "once" {
		t.Fatal("bad file")
	}
	// A claimed operation with no result must remain unknown, never executed.
	// Use an unresolved request: the installed Controller correctly retains
	// the verified private result of the completed request checked above.
	unknownPath := filepath.Join(filepath.Dir(path), "unknown-must-not-run.txt")
	b, _ = json.Marshal(map[string]string{"path": unknownPath, "content": "must not execute"})
	id := agentID(p.trace.Account, p.trace.Thread, p.trace.TraceID, "server_write", string(b))
	next.mu.Lock()
	op := agentOperation{ID: id, Tool: "server_write", Thread: p.trace.Thread, Arguments: b, Result: `{"state":"unknown"}`, State: "running", At: time.Now()}
	next.data.Operations[id] = op
	next.persist()
	next.mu.Unlock()
	result, err := p.Call(context.Background(), "server_write", b)
	if err != nil || !strings.Contains(result, "unknown") {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(unknownPath); !os.IsNotExist(err) {
		t.Fatal("unknown operation executed", err)
	}
}
func TestAgentSettingsAPIAuthenticationAndOptimisticSave(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/agent", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	c := f.manager.agent.config()
	if code, _, _ := f.request("POST", "/api/agent/config", c, true, false); code != 403 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", "/api/agent/config", c, true, true); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", "/api/agent/config", c, true, true); code != 409 {
		t.Fatal("stale configuration overwritten", code)
	}
	c = f.manager.agent.config()
	c.Host.Mode = "full"
	if code, _, _ := f.request("POST", "/api/agent/config", c, true, true); code != 200 {
		t.Fatal("isolated conversation execution needs no owner escalation", code)
	}
}

type crossTextNative struct{ *fakeTools }

func (n *crossTextNative) LookupSend(ctx context.Context, key string) (linuxnative.ToolStatus, error) {
	if n.submits.Load() == 0 {
		return linuxnative.ToolStatus{}, linuxnative.ErrToolNotFound
	}
	return n.fakeTools.LookupSend(ctx, key)
}
func TestAgentCrossSendReceiptInspectionAndMemory(t *testing.T) {
	f, p := trustedAgent(t)
	native := &crossTextNative{&fakeTools{fakeNative: f.native}}
	f.manager.native = native
	c := f.manager.agent.config()
	c.CrossConversation = true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.config.Behavior.RandomDelay = false
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.mu.Unlock()
	observeOutboundSource(t, f, p, "请向文件传输助手发送：来自已授权测试的消息")
	args := json.RawMessage(`{"thread_id":"filehelper","text":"来自已授权测试的消息","request_key":"one-send"}`)
	result, err := p.Call(context.Background(), "wechat_send_text", args)
	if err != nil {
		t.Fatal(err)
	}
	var submitted struct {
		OperationID string `json:"operation_id"`
		State       string `json:"state"`
	}
	if json.Unmarshal([]byte(result), &submitted) != nil || submitted.OperationID == "" || submitted.State != "queued" {
		t.Fatal(result)
	}
	if _, err = p.Call(context.Background(), "wechat_send_text", args); err != nil || native.submits.Load() != 1 {
		t.Fatal("send replayed", err, native.submits.Load())
	}
	if _, err = p.Call(context.Background(), "wechat_send_text", json.RawMessage(`{"thread_id":"filehelper","text":"偷偷改成新内容","request_key":"one-send"}`)); err == nil || native.submits.Load() != 1 {
		t.Fatal("logical send key reused with changed body")
	}
	statusArgs, _ := json.Marshal(map[string]string{"operation_id": submitted.OperationID})
	status, err := p.Call(context.Background(), "wechat_operation_status", statusArgs)
	if err != nil || !strings.Contains(status, `"state":"accepted"`) {
		t.Fatal(status, err)
	}
	scope := f.manager.memory.Context(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"}, f.manager.Config().Companion, time.Now())
	if len(scope.Messages) != 1 || scope.Messages[0].Role != "assistant" || scope.Messages[0].Text != "来自已授权测试的消息" {
		t.Fatal("accepted tool send absent from memory", scope)
	}
	if _, err = p.Call(context.Background(), "wechat_operation_status", statusArgs); err != nil {
		t.Fatal(err)
	}
	scope = f.manager.memory.Context(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"}, f.manager.Config().Companion, time.Now())
	if len(scope.Messages) != 1 || native.submits.Load() != 1 {
		t.Fatal("receipt poll replayed send or duplicated memory")
	}
}
func TestAgentTaskScopedPersistenceRecoveryAndQuietBudget(t *testing.T) {
	f, p := trustedAgent(t)
	in := `{"key":"music","title":"找一首歌","checkpoint":"已经找到来源，稍后检查","state":"waiting","delay_seconds":30}`
	if _, err := p.Call(context.Background(), "agent_task", json.RawMessage(in)); err != nil {
		t.Fatal(err)
	}
	tasks := f.manager.agent.list("test", "wxid_owner")
	if len(tasks) != 1 {
		t.Fatal(tasks)
	}
	old := p.trace
	p.trace.Sender = "wxid_other"
	if _, err := p.Call(context.Background(), "agent_task", json.RawMessage(`{"id":"`+tasks[0].ID+`","checkpoint":"hijack","state":"cancelled"}`)); err == nil {
		t.Fatal("task hijacked")
	}
	p.trace = old
	if len(f.manager.agent.list("test", "elsewhere")) != 0 {
		t.Fatal("scope escaped")
	}
	task := tasks[0]
	task.State = "running"
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	recovered, err := newAgentControl(f.dir)
	if err != nil || recovered.list("test", "")[0].State != "needs_review" {
		t.Fatal("running effect automatically resumed", err)
	}
	cfg := f.manager.Config()
	cfg.Companion.Enabled = true
	cfg.Companion.Permissions.Remember = true
	cfg.Companion.Permissions.ReplyText = true
	cfg.Companion.Permissions.Proactive = true
	cfg.Companion.ProactiveThreads = []string{"wxid_owner"}
	now := time.Now()
	zone, _ := time.LoadLocation(cfg.Companion.Timezone)
	local := now.In(zone)
	cfg.Companion.QuietStart = local.Add(-time.Hour).Format("15:04")
	cfg.Companion.QuietEnd = local.Add(time.Hour).Format("15:04")
	f.manager.mu.Lock()
	f.manager.config = cfg.Config
	f.manager.mu.Unlock()
	task.State = "waiting"
	task.OwnerRequested = false
	task.Next = now.Add(-time.Minute)
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	ownerTask, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || !ownerTask.OwnerRequested {
		t.Fatal("saved owner authorization was downgraded", ownerTask)
	}
	// Keep the original requested task intact but parked. Quiet-hour behavior
	// belongs to a genuinely separate legacy autonomous task, not a downgrade
	// of the authenticated owner's persisted request.
	ownerTask.State = "needs_review"
	if err := f.manager.agent.task(ownerTask); err != nil {
		t.Fatal(err)
	}
	task.ID += "-legacy-proactive"
	task.Constraints = nil
	task.OwnerRequested = false
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	if result, err := f.manager.claimAgentTask(now, func(string) bool { return false }); err != nil || result != nil {
		t.Fatal("quiet task ran", result, err)
	}
	_, count := f.manager.memory.Counters(cfg.Companion, now)
	if count != 0 {
		t.Fatal("quiet period charged")
	}
	// Outside quiet hours, only a selected, idle thread may claim the task;
	// the claim is persisted before execution and is not offered twice.
	cfg.Companion.QuietStart = "00:00"
	cfg.Companion.QuietEnd = "00:00"
	f.manager.mu.Lock()
	f.manager.config = cfg.Config
	f.manager.mu.Unlock()
	if err := f.manager.memory.Observe(p.trace, cfg.Companion, now); err != nil {
		t.Fatal(err)
	}
	if in, err := f.manager.claimAgentTask(now, func(string) bool { return true }); err != nil || in != nil {
		t.Fatal("busy thread claimed", in, err)
	}
	claimed, err := f.manager.claimAgentTask(now, func(string) bool { return false })
	if err != nil || claimed == nil || claimed.TaskID != task.ID {
		t.Fatal("task not resumed", claimed, err)
	}
	if again, err := f.manager.claimAgentTask(now, func(string) bool { return false }); err != nil || again != nil {
		t.Fatal("running task reclaimed", again, err)
	}
	f.manager.finishAgentTask(*claimed, "silent")
	if settled, ok := f.manager.agent.lookupTask(claimed.TaskID, "test", "wxid_owner"); !ok || settled.State != "stopped" || f.manager.agentTaskActive(*claimed) {
		t.Fatal("silent uncheckpointed task left active", settled)
	}
}
