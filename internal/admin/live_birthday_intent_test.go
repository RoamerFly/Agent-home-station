package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestLiveBirthdayMeansExistingSungSongAndUnrelatedChatDoesNotCancel(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_BIRTHDAY_LIVE") != "1" {
		t.Skip("explicit real model, synthetic song/bridge; no WeChat sends")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.config = cfg
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendVoice = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "requester", TraceID: "birthday-intent", Mode: "direct", Directed: true, Text: "大家正在祝寿星生日快乐。大肥鱼唱个生日歌来听一下。是常见生日快乐歌，保留原有旋律歌词唱给大家听，不是念祝福。"}
	a, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: syntheticReferenceWAV(t, 120), MIME: "audio/wav", Filename: "生日快乐-原曲.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	posts := 0
	var body map[string]any
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":6,"voice_conversion_ready":true}`))
			return
		}
		posts++
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"id":"birthday-model-fixture","state":"queued"}`))
	}))
	defer u.Close()
	f.manager.speech.YuEEnabled = true
	f.manager.speech.YuEEndpoint = u.URL
	f.manager.speech.YuEToken = "synthetic"
	p, _, _ := f.manager.toolAccess()
	observed := &coverLiveTools{p: p, transcript: "祝你生日快乐\n祝你生日快乐\n祝你生日快乐\n祝你生日快乐"}
	provider, err := f.manager.makeModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(cfg.Companion, f.manager.memory, provider, cfg.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = observed
	engine.MaxToolCalls = 8
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	decision, err := engine.Respond(ctx, in)
	tasks := f.manager.agent.list("test", "filehelper")
	report := map[string]any{"actual_generation": false, "wechat_sends": 0, "calls": observed.calls, "decision": decision, "submissions": posts, "mode": body["mode"]}
	if err != nil || posts != 1 || len(tasks) != 1 || tasks[0].MusicSpec.Mode != "cover" || tasks[0].MusicSpec.Reference != a.ID || tasks[0].MusicKind != "voice" {
		t.Errorf("birthday choice err=%v posts=%d tasks=%+v calls=%v", err, posts, tasks, observed.calls)
	}
	review, err := engine.ReviewPending(ctx, in, nil, []string{"正在准备生日歌的实际演唱音频，未交付"}, []companion.ConversationEvent{{MessageID: "emoji", Sender: "other", Text: "[偷笑][偷笑]"}, {MessageID: "offline", Sender: "third", Text: "大肥鱼还没开机"}})
	report["review"] = review
	if err != nil || review.Action != "continue" {
		t.Errorf("unrelated chat cancelled birthday: %+v %v", review, err)
	}
	if workspace := os.Getenv("WECHAT_LIVE_WORKSPACE"); workspace != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(workspace, "research/evidence/birthday-intent-live.json"), b, 0600)
	}
}
