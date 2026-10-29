package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type localMusicProbe struct {
	p        *permittedTools
	calls    []string
	webCalls int
}

func (s *localMusicProbe) SetTrace(in companion.Input)             { s.p.SetTrace(in) }
func (s *localMusicProbe) AgentContext(ctx context.Context) string { return s.p.AgentContext(ctx) }
func (s *localMusicProbe) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, e := s.p.List(ctx)
	out := []mcptools.Tool{}
	for _, tool := range all {
		if tool.Name == "media_recent" || tool.Name == "music_rvc_start" {
			out = append(out, tool)
		}
	}
	out = append(out, agentTool("web_search", "Search online for a song only if no suitable original is cached.", `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`))
	return out, e
}
func (s *localMusicProbe) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	s.calls = append(s.calls, name)
	if name == "web_search" {
		s.webCalls++
		return "", errors.New("synthetic web search should be unnecessary for cached song")
	}
	return s.p.Call(ctx, name, args)
}

// Real model chooses between local catalogue and a synthetic online tool;
// conversion submission is a fake HTTP backend, never actual music or WeChat.
func TestLiveMusicUsesCachedOriginalBeforeOnlineSearch(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_LOCAL_MUSIC_LIVE") != "1" {
		t.Skip("explicit real-model local-first music check")
	}
	live, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f, p := trustedAgent(t)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "cached-cover", Mode: "direct", Directed: true, Text: "用默认声线翻唱《合成测试歌曲》"}
	p.SetTrace(in)
	a, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: syntheticReferenceWAV(t, 3), MIME: "audio/wav", Filename: "合成测试歌曲-原唱.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	posts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rvc-health" {
			w.Write([]byte(`{"ready":true,"bridge_version":12,"voice_profiles":true}`))
			return
		}
		if r.URL.Path != "/rvc-jobs" {
			w.WriteHeader(404)
			return
		}
		posts++
		w.Write([]byte(`{"id":"cached-song-conversion","state":"queued"}`))
	}))
	defer upstream.Close()
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, upstream.URL, "synthetic"
	model, err := f.manager.makeModel(live)
	if err != nil {
		t.Fatal(err)
	}
	c := live.Companion
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.Permissions.SendVoice = true
	c.Permissions.Remember = true
	f.manager.config.Companion.Permissions = c.Permissions
	listed, _ := p.List(context.Background())
	names := map[string]bool{}
	for _, tool := range listed {
		names[tool.Name] = true
	}
	if !names["media_recent"] || !names["music_rvc_start"] {
		t.Fatal("fixture tools unavailable", names)
	}
	engine, err := companion.New(c, f.manager.memory, model, live.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	probe := &localMusicProbe{p: p}
	engine.Tools = probe
	engine.MaxToolCalls = agentTurnToolLimit
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err = engine.Respond(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	tasks := f.manager.agent.list(in.Account, in.Thread)
	if len(probe.calls) < 2 || probe.calls[0] != "media_recent" || probe.webCalls != 0 || posts != 1 || len(tasks) != 1 || tasks[0].MusicSpec.Reference != a.ID {
		t.Fatal("model bypassed local original", probe.calls, posts, tasks)
	}
	t.Logf("real_model=true local_lookup_first=true reused_original=true online_calls=0 synthetic_submissions=%d wechat_sends=0", posts)
}
