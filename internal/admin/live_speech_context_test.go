package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type mediaReferenceTools struct {
	chosen string
	calls  []string
}

type liveSongStaging struct{ *fakeTools }

func (*liveSongStaging) Capabilities(context.Context) (linuxnative.Capabilities, error) {
	return linuxnative.Capabilities{MediaKinds: []string{"file"}, MediaSendMaxBytes: map[string]int64{"file": 64 << 20}}, nil
}

func TestLiveCompletedYuESongStagesOriginalFile(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_SPEECH_CONTEXT_LIVE") != "1" {
		t.Skip("explicit completed local song read; no new generation or WeChat send")
	}
	jobData, err := os.ReadFile(filepath.Join(workspace, "research/evidence/yue-reference-job.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if json.Unmarshal(jobData, &evidence) != nil || evidence.Job.ID == "" {
		t.Fatal("completed reference job missing")
	}
	job := evidence.Job
	f := setup(t)
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.speech, err = loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	native := &liveSongStaging{&fakeTools{fakeNative: f.native}}
	f.manager.native = native
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "local-song-read"})
	ownedJob, _ := json.Marshal(job)
	f.manager.agent.data.Operations["isolated-owned-job"] = agentOperation{Tool: "music_local_start", Thread: "filehelper", State: "succeeded", Result: string(ownedJob)}
	raw, _ := json.Marshal(map[string]string{"job_id": job.ID, "kind": "file"})
	result, err := p.callYuE(context.Background(), "music_local_status", raw)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := f.manager.loadAssets()
	if err != nil || len(assets) != 1 || assets[0].Bytes < 100000 || assets[0].MIME != "audio/flac" || !strings.Contains(result, "media") || native.submits.Load() != 0 {
		t.Fatal("original song not staged", err, result)
	}
	report, _ := json.MarshalIndent(map[string]any{"job_id": job.ID, "prepared_kind": "file", "mime": assets[0].MIME, "original_bytes": assets[0].Bytes, "wechat_sends": native.submits.Load(), "new_generations": 0}, "", "  ")
	if err := os.WriteFile(filepath.Join(workspace, "research/evidence/live-yue-file-staging.json"), report, 0600); err != nil {
		t.Fatal(err)
	}
}

func (t *mediaReferenceTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{agentTool("media_recent", "Read recent media with original video source_url in current thread. Input {}.", `{"type":"object","properties":{}}`), agentTool("web_media_download", "Get original video file for a source URL. Input {url:string,kind:string,audio_only:boolean}.", `{"type":"object","properties":{"url":{"type":"string"},"kind":{"type":"string"},"audio_only":{"type":"boolean"}},"required":["url","kind"]}`), agentTool("web_search", "Search current Internet resources only when needed. Input {query:string}.", `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)}, nil
}
func (t *mediaReferenceTools) Call(_ context.Context, name string, raw json.RawMessage) (string, error) {
	t.calls = append(t.calls, name)
	if name == "media_recent" {
		return `[{"asset_id":"recent-song","title":"我们俩吉他弹唱～","source_url":"https://www.bilibili.com/video/BV1S1oeYbEe1/","formats":["voice","file"]}]`, nil
	}
	if name == "web_media_download" {
		var in struct {
			URL string `json:"url"`
		}
		json.Unmarshal(raw, &in)
		t.chosen = in.URL
		return `{"media":[{"id":"original-video","kind":"file","title":"我们俩原视频.mp4"}]}`, nil
	}
	return `{"results":[]}`, nil
}
func (t *mediaReferenceTools) Media(id string) (companion.GeneratedMedia, bool) {
	return companion.GeneratedMedia{ID: id, Kind: "file", Title: "我们俩原视频.mp4"}, id == "original-video"
}
func TestLiveNativeFunctionsResolveRecentOriginalVideo(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_SPEECH_CONTEXT_LIVE") != "1" {
		t.Skip("explicit isolated live model")
	}
	c, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	provider, e := f.manager.makeModel(c)
	if e != nil {
		t.Fatal(e)
	}
	cfg := c.Companion
	cfg.Enabled = true
	cfg.QuietStart = "00:00"
	cfg.QuietEnd = "00:00"
	cfg.Permissions.Remember = true
	cfg.Permissions.SendFile = true
	store, _ := companion.NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "test-room", Sender: "same-user", Mode: "direct", TraceID: "old", Text: "忠县高铁站什么时候运营"}
	store.Observe(in, cfg, time.Now().Add(-2*time.Hour))
	in.TraceID = "latest"
	in.Text = "这首弹唱挺好听"
	store.Observe(in, cfg, time.Now().Add(-10*time.Minute))
	store.ObserveMedia(in, cfg, companion.MediaMemory{Key: "latest-voice", Kind: "voice", Title: "我们俩吉他弹唱～.m4a", AssetID: "recent-song", URL: "https://www.bilibili.com/video/BV1S1oeYbEe1/", State: "accepted"}, time.Now().Add(-9*time.Minute))
	tools := &mediaReferenceTools{}
	engine, _ := companion.New(cfg, store, provider, c.SystemPrompt)
	engine.Tools = tools
	engine.MaxToolCalls = 12
	in.TraceID = "current"
	in.Text = "给我转发一下原视频"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d, e := engine.Respond(ctx, in)
	report := map[string]any{"decision": d, "error": e != nil, "chosen_url": tools.chosen, "tool_calls": tools.calls, "wechat_sends": 0, "native_functions": true}
	b, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(workspace, "research/evidence/native-functions-original-video.json"), b, 0600)
	if e != nil || tools.chosen != "https://www.bilibili.com/video/BV1S1oeYbEe1/" || d.Action != "reply" || len(d.MediaIDs) == 0 {
		t.Fatal("recent original reference wrong", d, e, tools.chosen, tools.calls)
	}
}
func TestLiveBailianSpeechRoundTrip(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_SPEECH_CONTEXT_LIVE") != "1" {
		t.Skip("explicit provider test")
	}
	c, e := loadSpeech(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	u, e := bailian.New(c).Synthesize(ctx, "你好呀，蓝色大肥鱼今天很开心。", "")
	if e != nil {
		t.Fatal(e)
	}
	audio, _, e := mcptools.GetPublicMedia(ctx, u, 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	wav, e := normalizeSpeech(ctx, t.TempDir(), audio)
	if e != nil {
		t.Fatal(e)
	}
	text, e := bailian.New(c).Transcribe(ctx, wav)
	report := map[string]any{"tts_model": c.TTSModel, "asr_model": c.ASRModel, "tts_bytes": len(audio), "asr_text": text, "wechat_sends": 0}
	b, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(workspace, "research/evidence/bailian-go-roundtrip.json"), b, 0600)
	if e != nil || !strings.Contains(text, "蓝色大肥鱼") {
		t.Fatal(text, e)
	}
}

func TestLiveModelSynthesizesAndPreparesWechatVoice(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_SPEECH_CONTEXT_LIVE") != "1" {
		t.Skip("explicit isolated TTS action")
	}
	cfg, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	speech, e := loadSpeech(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	f.manager.speech = speech
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.mu.Lock()
	f.manager.mcp.Enabled = false
	f.manager.mu.Unlock()
	provider, e := f.manager.makeModel(cfg)
	if e != nil {
		t.Fatal(e)
	}
	c := cfg.Companion
	c.Enabled = true
	c.Permissions.Remember = true
	c.Permissions.SendVoice = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	tools, _, e := f.manager.toolAccess()
	if e != nil {
		t.Fatal(e)
	}
	engine, _ := companion.New(c, f.manager.memory, provider, cfg.SystemPrompt)
	engine.Tools = tools
	engine.MaxToolCalls = 12
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d, e := engine.Respond(ctx, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "voice-live-test", Mode: "direct", Text: "请用你的声音说一句：今天很开心。只要一条说话语音，不要歌曲，也不要联网搜索。"})
	report := map[string]any{"decision": d, "error": e != nil, "prepared_voice": false, "wechat_sends": 0}
	for _, id := range d.MediaIDs {
		if media, ok := engine.Media(id); ok && media.Kind == "voice" {
			report["prepared_voice"] = true
		}
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(workspace, "research/evidence/live-model-tts-voice.json"), b, 0600)
	if e != nil || report["prepared_voice"] != true {
		t.Fatal(d, e)
	}
}

func TestLiveStructuredContextCompression(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_SPEECH_CONTEXT_LIVE") != "1" {
		t.Skip("explicit isolated summary test")
	}
	cfg, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	provider, e := f.manager.makeModel(cfg)
	if e != nil {
		t.Fatal(e)
	}
	c := cfg.Companion
	c.Permissions.Remember = true
	c.Context = &companion.ContextSettings{AutoCompress: true, CompressAfter: 20, RecentMessages: 5, MaxInputChars: 8000}
	store, _ := companion.NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "summary-test", Sender: "person-one"}
	for i := 0; i < 25; i++ {
		in.TraceID = time.Now().Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		in.Text = "我喜欢轻快的吉他音乐，旧的高铁站话题已经聊完。"
		store.Observe(in, c, time.Now())
	}
	engine, _ := companion.New(c, store, provider, cfg.SystemPrompt)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	changed, e := engine.CompressContext(ctx, in, true)
	archive, _ := store.Read(companion.ScopeID(in.Channel, in.Account, in.Thread), c, time.Now())
	active := store.Context(in, c, time.Now())
	report := map[string]any{"changed": changed, "error": e != nil, "archive_messages": len(archive.Messages), "compressed_messages": archive.CompressedCount, "active_raw_messages": len(active.Messages), "summary_valid_json": json.Valid([]byte(active.Summary)), "wechat_sends": 0}
	b, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(workspace, "research/evidence/live-context-compression.json"), b, 0600)
	if e != nil || !changed || len(archive.Messages) != 25 || len(active.Messages) != 5 || !json.Valid([]byte(active.Summary)) {
		t.Fatal(report, e)
	}
}
