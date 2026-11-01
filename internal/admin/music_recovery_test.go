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
	"wechat-bot/internal/linuxnative"
)

type recoverSongNative struct {
	*fakeNative
	createdAt string
}

func (n *recoverSongNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	return linuxnative.Message{ID: id, ThreadID: "filehelper", SenderID: "synthetic-owner", Direction: "incoming", Body: "唱个生日歌", CreatedAt: n.createdAt}, nil
}
func TestPreparedSongRecoveryUsesCompletedYuERecipeOnce(t *testing.T) {
	f := setup(t)
	f.manager.native = &recoverSongNative{fakeNative: f.native, createdAt: time.Now().Format(time.RFC3339)}
	f.manager.config.Companion.Permissions.SendVoice = true
	reads := 0
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("recovery submitted a generation")
		}
		reads++
		if reads > 1 {
			w.Write([]byte(`{"id":"prepared-song","state":"queued"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "prepared-song", "state": "completed", "reference_used": true, "recipe": map[string]any{"title": "生日快乐", "mode": "cover", "lyrics": "synthetic original words", "style": "source style", "duration_seconds": 60, "reference_sha256": strings.Repeat("a", 64)}})
	}))
	defer u.Close()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEnabled = true
		legacySpeechFixture.YuEEndpoint = u.URL
		legacySpeechFixture.YuEToken = "synthetic"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	installLegacyLocalMusicPlugin(t, f.manager)
	f.login()
	body := map[string]string{"message_id": "birthday-request", "prepared_job_id": "prepared-song"}
	code, b, _ := f.request("POST", "/api/agent/recover-music-message", body, true, true)
	if code != 201 {
		t.Fatal(code, string(b))
	}
	var first agentTask
	json.Unmarshal(b, &first)
	code, b, _ = f.request("POST", "/api/agent/recover-music-message", body, true, true)
	if code != 200 {
		t.Fatal(code, string(b))
	}
	var second agentTask
	json.Unmarshal(b, &second)
	if first.ID != second.ID || first.MusicJob != "prepared-song" || first.Thread != "filehelper" || first.MusicKind != "voice" || first.MusicSpec.Mode != "cover" || first.RequestedText != "唱个生日歌" {
		t.Fatal(first, second)
	}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-owner"})
	if _, err := p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"prepared-song"}`)); err != nil {
		t.Fatal("adopted scoped task could not read its output", err)
	}
	p.trace.Thread = "other-room"
	if _, err := p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"prepared-song"}`)); err == nil || reads != 2 {
		t.Fatal("adoption leaked across rooms", err, reads)
	}
}

func TestExplicitCorrectedTakePreservesOldDeliveryAndIsIdempotent(t *testing.T) {
	f := setup(t)
	f.manager.native = &recoverSongNative{fakeNative: f.native, createdAt: time.Now().Format(time.RFC3339)}
	f.manager.config.Companion.Permissions.SendVoice = true
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEnabled = true
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	old := agentTask{ID: "old-task", Account: "test", Thread: "filehelper", Sender: "synthetic-owner", OriginTrace: "birthday-request", MusicJob: "old-take", State: "completed"}
	if err := f.manager.agent.task(old); err != nil {
		t.Fatal(err)
	}
	reads := 0
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Fatal("unexpected mutation")
		}
		reads++
		json.NewEncoder(w).Encode(map[string]any{"id": "corrected-take", "state": "completed", "reference_used": true, "recipe": map[string]any{"title": "生日快乐", "mode": "cover", "lyrics": "synthetic words", "reference_sha256": strings.Repeat("b", 64), "duration_seconds": 180}})
	}))
	defer u.Close()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEndpoint, legacySpeechFixture.YuEToken = u.URL, "synthetic"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	installLegacyLocalMusicPlugin(t, f.manager)
	f.login()
	body := map[string]any{"message_id": "birthday-request", "prepared_job_id": "corrected-take", "redeliver_prepared": true}
	for i := 0; i < 2; i++ {
		code, b, _ := f.request("POST", "/api/agent/recover-music-message", body, true, true)
		if code != 201 && code != 200 {
			t.Fatal(code, string(b))
		}
		var got agentTask
		json.Unmarshal(b, &got)
		if got.MusicJob != "corrected-take" || got.ID == old.ID {
			t.Fatal(got)
		}
	}
	if reads != 1 {
		t.Fatal("duplicate adoption", reads)
	}
	all := f.manager.agent.list("test", "filehelper")
	if len(all) != 2 {
		t.Fatal(all)
	}
	for _, task := range all {
		if task.ID == old.ID && (task.State != "completed" || task.MusicJob != "old-take") {
			t.Fatal("old task was overwritten", task)
		}
	}
}
