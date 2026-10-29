package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type refinementObserved struct {
	*reuseObservedTools
	results []map[string]any
}

func (t *refinementObserved) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	result, err := t.reuseObservedTools.Call(ctx, name, args)
	if strings.HasPrefix(name, "web_") {
		t.results = append(t.results, map[string]any{"tool": name, "result": logText(result, 6000), "failed": err != nil})
	}
	return result, err
}

func TestLiveRestrainedSocialAndFreshNamedSong(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_CHAT_REFINEMENT") != "1" {
		t.Skip("explicit isolated social and public music diagnostic")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	report := []map[string]any{}
	for _, tc := range []struct{ mode, text string }{{"social", "全是英文，看不懂 😁"}, {"social", "设的兴趣搞错了 😁"}, {"social", "对，这叫娱乐兴趣 🤭"}, {"direct", "重新找一首，traveling light"}} {
		f := setup(t)
		native := &fakeTools{fakeNative: f.native}
		f.manager.native = native
		f.manager.config = c
		provider, err := f.manager.makeModel(c)
		if err != nil {
			t.Fatal(err)
		}
		config := c.Companion
		config.QuietStart = "00:00"
		config.QuietEnd = "00:00"
		config.Permissions.ObserveGroups = true
		config.Permissions.AutonomousGroups = true
		config.Permissions.Remember = true
		store, err := companion.NewStore(t.TempDir())
		if opened := store; opened != nil { t.Cleanup(func(){ _ = opened.Close() }) }
		if err != nil {
			t.Fatal(err)
		}
		seed := companion.Input{Channel: "isolated", Account: "test", Thread: "refinement-probe", Sender: "群友甲", Text: "这个兴趣设置有点学术"}
		if err = store.Observe(seed, config, time.Now().Add(-2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err = store.Accepted(seed, "刚才分享的是一篇英文量子论文。上一首语音是 Cloud Dancer。", config, time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		engine, err := companion.New(config, store, provider, c.SystemPrompt)
		if err != nil {
			t.Fatal(err)
		}
		access, max, err := f.manager.toolAccess()
		if err != nil {
			t.Fatal(err)
		}
		observed := &refinementObserved{reuseObservedTools: &reuseObservedTools{inner: access}}
		engine.Tools = observed
		engine.MaxToolCalls = max
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		d, err := engine.Respond(ctx, companion.Input{Channel: "isolated", Account: "test", Thread: "refinement-probe", Sender: "群友乙", Mode: tc.mode, IsGroup: true, Directed: tc.mode == "direct", Text: tc.text})
		cancel()
		activities := []map[string]any{}
		namedSearch := false
		for _, x := range f.manager.Logs() {
			if x.Event == "web_activity" && x.Status != "running" {
				activities = append(activities, map[string]any{"tool": x.Tool, "query": x.Query, "url": x.TargetURL, "status": x.Status, "bytes": x.Bytes, "requests": x.ToolRequests})
				if (x.Tool == "web_search" || x.Tool == "web_audio_search") && strings.Contains(strings.ToLower(x.Query), "traveling") {
					namedSearch = true
				}
			}
		}
		media := []companion.GeneratedMedia{}
		for _, id := range d.MediaIDs {
			if item, ok := engine.Media(id); ok {
				media = append(media, item)
			}
		}
		report = append(report, map[string]any{"mode": tc.mode, "input": tc.text, "decision": d, "error": err != nil, "tool_calls": observed.calls, "activities": activities, "public_results": observed.results, "selected_media": media, "wechat_sends": native.submits.Load()})
		if err != nil {
			t.Error(err)
		}
		if tc.mode == "social" && d.Action != "silent" && d.Action != "reply" {
			t.Error("model social choice was not honored", tc.text, d)
		}
		if tc.mode == "direct" && !namedSearch {
			t.Error("new named song was not searched", d, observed.calls)
		}
		if native.submits.Load() != 0 {
			t.Fatal("isolated probe sent to WeChat")
		}
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence/chat-refinement-live.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLiveNamedSongAudioFromVerifiedPublicPage(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_CHAT_REFINEMENT") != "1" {
		t.Skip("explicit isolated public audio extraction")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	f.manager.config.Companion = c.Companion
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	access, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	// This BV page was returned by the actual public search in the preceding
	// diagnostic. Never synthesize a media/CDN address from the song name.
	page := "https://www.bilibili.com/video/BV1425hzpEtn/"
	access.SetTrace(companion.Input{Channel: "isolated", Account: "test", Thread: "named-song-audio", Text: page})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	args, _ := json.Marshal(map[string]string{"url": page, "kind": "voice"})
	result, callErr := access.Call(ctx, "web_media_download", args)
	var prepared struct {
		Media []companion.GeneratedMedia `json:"media"`
		Text  string                     `json:"text"`
	}
	_ = json.Unmarshal([]byte(result), &prepared)
	bytes := int64(0)
	for _, x := range f.manager.Logs() {
		if x.Event == "web_activity" && x.Tool == "web_media_download" && x.Status == "succeeded" {
			bytes = x.Bytes
		}
	}
	report := map[string]any{"source_page": page, "error": callErr != nil, "downloaded_bytes": bytes, "media": prepared.Media, "wechat_sends": native.submits.Load(), "result": logText(result, 5000)}
	if callErr == nil && len(prepared.Media) > 0 {
		child := prepared.Media[0]
		assets, err := f.manager.loadAssets()
		if err != nil || child.ParentAssetID == "" || child.CachedMIME != "audio/mp4" {
			t.Fatal("missing real conversion lineage", err, child)
		}
		found := false
		for _, asset := range assets {
			if asset.ID != child.ParentAssetID {
				continue
			}
			probe, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-format_whitelist", "mov", "-show_entries", "stream=codec_type", "-of", "json", filepath.Join(f.dir, "media-assets", asset.Hash)).Output()
			var streams struct {
				Streams []struct {
					Kind string `json:"codec_type"`
				} `json:"streams"`
			}
			if err != nil || json.Unmarshal(probe, &streams) != nil {
				t.Fatal("cached parent probe failed", err)
			}
			for _, stream := range streams.Streams {
				found = found || stream.Kind == "video"
			}
			report["cached_original_video_bytes"] = asset.Bytes
		}
		if !found {
			t.Fatal("audio-only cache mislabeled as original video")
		}
		reused, err := access.reuseAsset(ctx, child.ParentAssetID, "file")
		if err != nil || !strings.Contains(reused, `"cached_mime":"video/mp4"`) || !strings.Contains(reused, `"network_downloads":0`) {
			t.Fatal("original video reuse failed", err)
		}
		report["original_video_reused_without_download"] = true
		report["native_staging"] = "simulated; actual public download/extraction/cache"
	}
	if callErr != nil {
		report["error_reason"] = mcptools.FailureDescription(callErr)
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence/named-song-audio-live.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if callErr != nil || bytes < 10000 || len(prepared.Media) == 0 || native.submits.Load() != 0 {
		t.Fatal("public audio extraction did not complete", callErr, bytes)
	}
	for _, m := range prepared.Media {
		if m.Kind != "voice" {
			t.Fatal("audio not prepared as voice", m)
		}
	}
}
