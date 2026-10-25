package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/companion"
)

func TestMusicFollowupPersistsPollsAndDeliversWithoutProactivePermission(t *testing.T) {
	state := "running"
	starts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-fixture-token" {
			t.Error("missing token")
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
	c := bailian.Defaults()
	c.YuEEnabled = true
	c.YuEEndpoint = server.URL
	c.YuEToken = "private-fixture-token"
	f.manager.speech = c
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
	if task = f.manager.agent.list("test", "filehelper")[0]; task.State != "completed" || !task.MusicPrepared {
		t.Fatal("delivery checkpoint missing", task)
	}
	if again, _ := f.manager.claimAgentTask(now.Add(4*time.Minute), func(string) bool { return false }); again != nil || starts != 1 {
		t.Fatal("completed task replayed")
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
