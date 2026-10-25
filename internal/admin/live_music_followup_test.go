package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

func TestLiveStyleConfirmationStartsYuEAndReturnsCompletedSong(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_MUSIC_FOLLOWUP_LIVE") != "1" {
		t.Skip("explicit local song generation with fake WeChat transport")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.config = cfg
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.Proactive = false
	f.manager.config.Companion.ProactiveThreads = nil
	f.manager.config.Companion.ProactiveRules = nil
	f.manager.config.Companion.QuietStart = "00:00"
	f.manager.config.Companion.QuietEnd = "00:00"
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.speech, err = loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	// Avoid unrelated Internet/provider tools in this execution regression.
	f.manager.mcp.Enabled = false
	provider, err := f.manager.makeModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := f.manager.config.Companion
	history := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", Mode: "direct", TraceID: "music-context", Text: "给我唱一首原创歌"}
	f.manager.memory.Observe(history, c, time.Now().Add(-time.Minute))
	f.manager.memory.Accepted(history, "可以呀，想听什么风格？", c, time.Now().Add(-50*time.Second))
	in := history
	in.TraceID = "style-confirmation-live-20261025"
	in.Text = "可以，忧郁风格吧"
	tools, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(c, f.manager.memory, provider, cfg.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = tools
	engine.MaxToolCalls = 12
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	decision, err := engine.Respond(ctx, in)
	report := map[string]any{"initial_decision": decision, "wechat_sends": 0, "proactive_permission": false}
	if err != nil {
		report["initial_error"] = err.Error()
	}
	save := func() {
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(workspace, "research/evidence/music-followup-live.json"), b, 0600)
	}
	tasks := f.manager.agent.list("test", "filehelper")
	report["registered_tasks"] = len(tasks)
	save()
	if err != nil || len(tasks) != 1 || tasks[0].MusicJob == "" {
		t.Fatal("style confirmation did not start generation", err, decision)
	}
	job := tasks[0].MusicJob
	report["job_id"] = job
	save()
	for {
		if ctx.Err() != nil {
			t.Fatal("generation deadline", ctx.Err())
		}
		data, _, e := tools.yueRequest(ctx, "GET", "/jobs/"+job, nil, "")
		if e != nil {
			t.Fatal(e)
		}
		var status struct{ State string }
		json.Unmarshal(data, &status)
		if status.State == "completed" {
			break
		}
		if status.State == "failed" || status.State == "needs_review" {
			report["generation_state"] = status.State
			save()
			t.Fatal(status.State)
		}
		time.Sleep(2 * time.Second)
	}
	followup, err := f.manager.claimAgentTask(time.Now().Add(time.Minute), func(string) bool { return false })
	if err != nil || followup == nil {
		t.Fatal("completion not scheduled", err)
	}
	tools, _, _ = f.manager.toolAccess()
	engine.Tools = tools
	completed, err := engine.Respond(ctx, *followup)
	report["completion_decision"] = completed
	report["generation_state"] = "completed"
	report["completion_mode"] = followup.Mode
	if err != nil {
		report["completion_error"] = err.Error()
	}
	save()
	if err != nil || len(completed.MediaIDs) == 0 {
		t.Fatal("result not delivered", err, completed)
	}
	f.manager.finishAgentTask(*followup, completed.Action)
	report["task_state"] = f.manager.agent.list("test", "filehelper")[0].State
	report["song_prepared"] = true
	save()
	if report["task_state"] != "waiting_delivery" {
		t.Fatal("task did not enter delivery tracking")
	}
}

type musicResponseTrace struct {
	*deepseek.Client
	Responses []json.RawMessage
}

func (m *musicResponseTrace) ChatFunctions(ctx context.Context, s string, t []map[string]string, tools []mcptools.Tool) (string, error) {
	result, err := m.Client.ChatFunctions(ctx, s, t, tools)
	if json.Valid([]byte(result)) {
		m.Responses = append(m.Responses, json.RawMessage(result))
	}
	return result, err
}

func TestLiveCompletedMusicContinuationSelectsStagedMedia(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_MUSIC_FOLLOWUP_LIVE") != "1" {
		t.Skip("existing completed song only; no regeneration or sends")
	}
	previous, _ := os.ReadFile(filepath.Join(workspace, "research/evidence/music-followup-live.json"))
	var existing struct {
		ID string `json:"job_id"`
	}
	json.Unmarshal(previous, &existing)
	if existing.ID == "" {
		t.Fatal("existing completed test song required")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.config = cfg
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Proactive = false
	f.manager.config.Companion.QuietStart = "00:00"
	f.manager.config.Companion.QuietEnd = "00:00"
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.speech, err = loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.mcp.Enabled = false
	owned, _ := json.Marshal(map[string]string{"id": existing.ID})
	f.manager.agent.data.Operations["existing-song"] = agentOperation{Thread: "filehelper", Tool: "music_local_start", Result: string(owned), State: "completed"}
	task := agentTask{ID: "existing-song-task", Account: "test", Thread: "filehelper", Sender: "owner", MusicJob: existing.ID, MusicKind: "voice", State: "waiting", Title: "忧郁风格原创歌曲", Next: time.Now().Add(-time.Minute), MusicDeadline: time.Now().Add(time.Hour)}
	f.manager.agent.task(task)
	input, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
	if err != nil || input == nil {
		t.Fatal(err)
	}
	provider, err := f.manager.makeModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	model := &musicResponseTrace{Client: provider}
	tools, _, _ := f.manager.toolAccess()
	engine, err := companion.New(f.manager.config.Companion, f.manager.memory, model, cfg.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = tools
	engine.MaxToolCalls = 12
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	decision, err := engine.Respond(ctx, *input)
	report := map[string]any{"decision": decision, "responses": model.Responses, "staged_media": tools.media, "permissions": f.manager.config.Companion.Permissions, "wechat_sends": 0, "new_generations": 0}
	if err != nil {
		report["error"] = err.Error()
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(workspace, "research/evidence/music-followup-completion.json"), b, 0600)
	if err != nil || len(decision.MediaIDs) == 0 {
		t.Fatal("completion failed", err, decision)
	}
	f.manager.finishAgentTask(*input, decision.Action)
}
