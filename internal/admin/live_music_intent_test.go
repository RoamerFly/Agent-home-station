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

func TestLiveMusicSemanticIntentWithoutGenerationOrWechatSend(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_MUSIC_INTENT_LIVE") != "1" {
		t.Skip("explicit live intent validation only; mock generator and transport")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	report := []map[string]any{}
	for index, tc := range []struct {
		text, kind string
		duration   int
	}{
		{"给我唱一首两分钟左右的，适合下班听的中文歌曲", "voice", 120},
		{"生成后唱给我听并发送文件给我，轻松下班的中文歌，一分钟左右", "both", 60},
		{"帮我生成一首夜晚放松的中文歌，只发文件，不用语音，60秒", "file", 60},
	} {
		f := setup(t)
		f.manager.config = cfg
		f.manager.config.Companion.Enabled = true
		f.manager.config.Companion.Permissions.SendVoice = true
		f.manager.config.Companion.Permissions.SendFile = true
		f.manager.config.Companion.Permissions.Remember = true
		f.manager.native = &fakeTools{fakeNative: f.native}
		f.manager.mcp.Enabled = false
		provider, e := f.manager.makeModel(cfg)
		if e != nil {
			t.Fatal(e)
		}
		posts := 0
		var submitted struct {
			Title    string
			Duration int `json:"duration_seconds"`
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			posts++
			json.NewDecoder(r.Body).Decode(&submitted)
			w.Write([]byte(`{"id":"semantic-job","state":"queued"}`))
		}))
		f.manager.speech.YuEEnabled = true
		f.manager.speech.YuEEndpoint = server.URL
		f.manager.speech.YuEToken = "private-fixture-token"
		tools, _, _ := f.manager.toolAccess()
		engine, e := companion.New(f.manager.config.Companion, f.manager.memory, provider, cfg.SystemPrompt)
		if e != nil {
			t.Fatal(e)
		}
		engine.Tools = tools
		engine.MaxToolCalls = 8
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		d, e := engine.Respond(ctx, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "semantic-case", Mode: "direct", Text: tc.text})
		cancel()
		server.Close()
		tasks := f.manager.agent.list("test", "filehelper")
		actualKind := ""
		if len(tasks) > 0 {
			actualKind = tasks[0].MusicKind
		}
		report = append(report, map[string]any{"case": index, "request": tc.text, "decision": d, "posts": posts, "title": submitted.Title, "duration": submitted.Duration, "kind": actualKind, "wechat_sends": 0, "actual_generation": false})
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(workspace, "research/evidence/music-semantic-intent-live.json"), b, 0600)
		if e != nil || posts != 1 || actualKind != tc.kind || submitted.Duration != tc.duration || songTitle(submitted.Title) == "" {
			t.Fatalf("case %d: err=%v posts=%d kind=%s title=%s duration=%d", index, e, posts, actualKind, submitted.Title, submitted.Duration)
		}
	}
	f := setup(t)
	provider, e := f.manager.makeModel(cfg)
	if e != nil {
		t.Fatal(e)
	}
	checker := provider
	for _, tc := range []struct {
		reply string
		want  bool
	}{
		{"好呀，下班路上听的歌这就安排～ 我写首轻快带点松弛的中文原创，大概两分钟 🎵", true},
		{"想要轻快一点还是安静一点的风格呀？", false},
		{"这次生成失败了，暂时没有歌曲可以发送。", false},
	} {
		got, e := checker.VerifyMusicPromise(context.Background(), "给我唱一首下班听的中文歌", tc.reply)
		if e != nil || got != tc.want {
			t.Fatal("semantic promise classification", e, got, tc.want)
		}
	}
}
