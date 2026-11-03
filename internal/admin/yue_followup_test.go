package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mediaspec"
)

func TestMusicFollowupPersistsPollsAndDeliversWithoutProactivePermission(t *testing.T) {
	state := "running"
	starts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-fixture-token" {
			t.Error("missing token")
		}
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":7}`))
			return
		}
		if r.Method == "POST" {
			starts++
			w.Write([]byte(`{"id":"fixture-job","state":"queued"}`))
			return
		}
		if r.URL.Path == "/jobs/fixture-job/audio" {
			w.Header().Set("Content-Type", "audio/flac")
			w.Write([]byte("fLaCfixture-original-audio"))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "fixture-job", "state": state})
	}))
	defer server.Close()
	f := setup(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	cfg := f.manager.config
	cfg.Companion.Enabled = true
	cfg.Companion.Permissions.Remember = true
	cfg.Companion.Permissions.Proactive = false
	cfg.Companion.Permissions.SendFile = true
	cfg.Companion.ProactiveThreads = nil
	zone, _ := time.LoadLocation(cfg.Companion.Timezone)
	now := time.Now()
	cfg.Companion.QuietStart = now.In(zone).Add(-time.Hour).Format("15:04")
	cfg.Companion.QuietEnd = now.In(zone).Add(time.Hour).Format("15:04")
	f.manager.config = cfg
	c := mediaspec.Defaults()
	c.YuEEnabled = true
	c.YuEEndpoint = server.URL
	c.YuEToken = "private-fixture-token"
	writeLegacySpeechMigrationFixture(t, f.manager, c)
	installLegacyLocalMusicPlugin(t, f.manager)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "requested-song"})
	raw := json.RawMessage(`{"title":"晚风下班路","style":"melancholic female folk","lyrics":"[verse]\n雨落窗台\n[chorus]\n也会放晴","kind":"file"}`)
	result, err := p.Call(context.Background(), "music_local_start", raw)
	if err != nil {
		t.Fatal(err)
	}
	var job struct {
		Automatic bool `json:"automatic_followup"`
	}
	json.Unmarshal([]byte(result), &job)
	if !job.Automatic || len(f.manager.agent.list("test", "filehelper")) != 1 {
		t.Fatal("followup not persisted", result)
	}
	if _, err = p.Call(context.Background(), "music_local_start", raw); err != nil || starts != 1 {
		t.Fatal("duplicate generation", err, starts)
	}
	recovered, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.agent = recovered
	in, err := f.manager.claimAgentTask(now.Add(time.Minute), func(string) bool { return false })
	if err != nil || in != nil {
		t.Fatal("unfinished job woke model", err, in)
	}
	task := f.manager.agent.list("test", "filehelper")[0]
	if task.Runs != 0 || task.State != "waiting" {
		t.Fatal(task)
	}
	state = "completed"
	in, err = f.manager.claimAgentTask(now.Add(2*time.Minute), func(string) bool { return false })
	if err != nil || in == nil || in.Mode != "direct" || in.TaskID != task.ID {
		t.Fatal("requested completion blocked by proactive or quiet", err, in)
	}
	if again, _ := f.manager.claimAgentTask(now.Add(3*time.Minute), func(string) bool { return false }); again != nil {
		t.Fatal("completion claimed twice")
	}
	p, _, _ = f.manager.toolAccess()
	p.SetTrace(*in)
	if _, err = p.Call(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"fixture-job","kind":"file"}`)); err != nil {
		t.Fatal(err)
	}
	f.manager.finishAgentTask(*in, "reply")
	if task = f.manager.agent.list("test", "filehelper")[0]; task.State != "waiting_delivery" || !task.MusicPrepared {
		t.Fatal("delivery checkpoint missing", task)
	}
	if again, _ := f.manager.claimAgentTask(now.Add(4*time.Minute), func(string) bool { return false }); again != nil || starts != 1 {
		t.Fatal("completed task replayed")
	}
}

func TestParentReadOfCompletedChildDoesNotStageAudioOrResume(t *testing.T) {
	statusReads, audioReads, resumes := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jobs/child-rvc-job":
			statusReads++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"child-rvc-job","engine":"rvc","state":"completed"}`))
		case "/jobs/child-rvc-job/audio":
			audioReads++
			w.WriteHeader(http.StatusInternalServerError)
		case "/jobs/child-rvc-job/resume":
			resumes++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	f := setup(t)
	c := mediaspec.Defaults()
	c.YuEEnabled, c.YuEEndpoint, c.YuEToken = true, server.URL, "private-fixture-token"
	writeLegacySpeechMigrationFixture(t, f.manager, c)
	installLegacyLocalMusicPlugin(t, f.manager)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "parent-origin", TaskID: "parent-task", Mode: "direct", Text: "用 mom 翻唱"}
	p.SetTrace(in)
	parent := agentTask{ID: in.TaskID, Account: in.Account, Thread: in.Thread, Sender: in.Sender, State: "running", OriginTrace: in.TraceID}
	child := agentTask{ID: "child-task", ParentID: parent.ID, Account: parent.Account, Thread: parent.Thread, Sender: parent.Sender, State: "waiting", OriginTrace: parent.OriginTrace, MusicJob: "child-rvc-job", MusicKind: "file", MusicSpec: &songSpec{Engine: "rvc"}}
	for _, task := range []agentTask{parent, child} {
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	f.manager.agent.mu.Lock()
	f.manager.agent.data.Operations["child-rvc-operation"] = agentOperation{ID: "child-rvc-operation", Thread: in.Thread, Tool: "music_rvc_start", Result: `{"id":"child-rvc-job"}`, State: "completed", At: time.Now()}
	f.manager.agent.mu.Unlock()
	status, err := p.callYuE(t.Context(), "music_local_status", json.RawMessage(`{"job_id":"child-rvc-job","kind":"file"}`))
	if err != nil || !strings.Contains(status, `"child_task_id":"child-task"`) || !strings.Contains(status, `"automatic_followup":true`) {
		t.Fatal(status, err)
	}
	if statusReads != 1 || audioReads != 0 || resumes != 0 {
		t.Fatalf("parent read submitted protected child work: status=%d audio=%d resume=%d", statusReads, audioReads, resumes)
	}
	if _, err := p.callYuE(t.Context(), "music_local_resume", json.RawMessage(`{"job_id":"child-rvc-job"}`)); err == nil || !strings.Contains(err.Error(), "子任务") {
		t.Fatalf("parent resumed child work: %v", err)
	}
	if statusReads != 1 || audioReads != 0 || resumes != 0 {
		t.Fatalf("parent resume touched child after rejection: status=%d audio=%d resume=%d", statusReads, audioReads, resumes)
	}
}

func TestPreparedAssetAliasIsScopedUnambiguousAndResettable(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	p.media["source-one"] = companion.GeneratedMedia{ID: "source-one", AssetID: "asset-one", Kind: "voice"}
	if media, ok := p.Media("asset-one"); !ok || media.ID != "source-one" {
		t.Fatal("valid staged alias rejected")
	}
	if _, ok := p.Media("historic-unprepared-asset"); ok {
		t.Fatal("unstaged asset allowed")
	}
	p.media["source-two"] = companion.GeneratedMedia{ID: "source-two", AssetID: "asset-one", Kind: "file"}
	if _, ok := p.Media("asset-one"); ok {
		t.Fatal("ambiguous formats allowed")
	}
	p.ResetMedia()
	if _, ok := p.Media("asset-one"); ok {
		t.Fatal("previous-run asset allowed")
	}
}
