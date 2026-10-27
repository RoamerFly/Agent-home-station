package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// Real model, synthetic source audio/lyrics and generator. Never opens the
// production task/memory stores and never sends a WeChat message.
func TestLiveNamedSongContextChoosesFemaleCoverDespiteOldMaleHistory(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_SONG_CONTEXT_LIVE") != "1" {
		t.Skip("opt-in isolated real-model context test")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendVoice = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "song-context-current", Sender: "alice", Mode: "direct", Directed: true, Text: "给我唱一首“朋友的酒”"}
	old := in
	old.TraceID, old.Text = "old-unrelated", "旧任务当时用了male vocal和original；那是已经结束的历史录音。"
	f.manager.memory.Observe(old, f.manager.config.Companion, time.Now().Add(-time.Minute))
	a, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte("synthetic source audio, not real music"), MIME: "audio/wav", Filename: "朋友的酒-原录音.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.memory.ObserveMedia(old, f.manager.config.Companion, companion.MediaMemory{Key: "synthetic-source", AssetID: a.ID, Title: "朋友的酒-用户提供的原录音", Kind: "voice", State: "observed"}, time.Now().Add(-time.Second))
	var submitted map[string]any
	starts := 0
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":4}`))
			return
		}
		starts++
		json.NewDecoder(r.Body).Decode(&submitted)
		w.Write([]byte(`{"id":"synthetic-female-cover","state":"queued"}`))
	}))
	defer u.Close()
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, u.URL, "synthetic-token"
	p, _, _ := f.manager.toolAccess()
	tools := &coverLiveTools{p: p, transcript: "[Verse]\n合成验证歌词\n[Chorus]\n晚风轻轻吹"}
	provider, err := f.manager.makeModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e, err := companion.New(f.manager.config.Companion, f.manager.memory, provider, cfg.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = tools, 12
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	d, err := e.Respond(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || submitted["mode"] != "cover" || submitted["vocal_gender"] != "female" || submitted["singing_voice"] != "native" || !strings.Contains(submitted["style"].(string), "保持原曲调和原歌词") || !strings.Contains(submitted["lyrics"].(string), "合成验证歌词") {
		t.Fatalf("wrong isolated model plan: starts=%d mode=%v gender=%v voice=%v calls=%v decision=%s", starts, submitted["mode"], submitted["vocal_gender"], submitted["singing_voice"], tools.calls, d.Action)
	}
	t.Logf("real model chose female native cover with source/evidence; calls=%v; generator mocked; WeChat sends=0", tools.calls)
}
