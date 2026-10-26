package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
)

type recoverSongNative struct{ *fakeNative }

func (n *recoverSongNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	return linuxnative.Message{ID: id, ThreadID: "filehelper", SenderID: "synthetic-owner", Direction: "incoming", Body: "唱个生日歌", CreatedAt: time.Now().Format(time.RFC3339)}, nil
}
func TestPreparedSongRecoveryUsesCompletedYuERecipeOnce(t *testing.T) {
	f := setup(t)
	f.manager.native = &recoverSongNative{f.native}
	f.manager.config.Companion.Permissions.SendVoice = true
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("recovery submitted a generation")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "prepared-song", "state": "completed", "reference_used": true, "recipe": map[string]any{"title": "生日快乐", "mode": "cover", "lyrics": "synthetic original words", "style": "source style", "duration_seconds": 60, "reference_sha256": strings.Repeat("a", 64)}})
	}))
	defer u.Close()
	f.manager.speech.YuEEnabled = true
	f.manager.speech.YuEEndpoint = u.URL
	f.manager.speech.YuEToken = "synthetic"
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
}
