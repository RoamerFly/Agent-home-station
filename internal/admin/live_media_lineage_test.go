package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type lineageObservedTools struct {
	*reuseObservedTools
	steps []map[string]any
}

func (t *lineageObservedTools) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	result, err := t.reuseObservedTools.Call(ctx, name, args)
	t.steps = append(t.steps, map[string]any{"tool": name, "arguments": json.RawMessage(args), "error": err != nil, "result": logText(result, 3500)})
	return result, err
}

// Real model, synthetic conversations/assets, fake native staging, no WeChat
// sends or public searches. A passing case is evidence of model choice only.
func TestLiveModelResolvesRequesterAndQuotedVideoParent(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_MEDIA_LINEAGE_LIVE") != "1" {
		t.Skip("explicit isolated model evaluation")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	reports := []map[string]any{}
	for _, scenario := range []string{"requesters_audio_file", "quoted_other_users_video", "whole_voice_bundle"} {
		f := setup(t)
		native := &fakeTools{fakeNative: f.native}
		f.manager.native = native
		cfgCopy := cfg.Companion
		cfgCopy.Enabled, cfgCopy.Permissions.Remember, cfgCopy.Permissions.SendFile, cfgCopy.Permissions.SendVoice = true, true, true, true
		cfgCopy.QuietStart, cfgCopy.QuietEnd = "00:00", "00:00"
		f.manager.config.Companion = cfgCopy
		provider, err := f.manager.makeModel(cfg)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := companion.New(cfgCopy, f.manager.memory, provider, cfg.SystemPrompt)
		if err != nil {
			t.Fatal(err)
		}
		alice := companion.Input{Channel: "isolated", Account: "test", Thread: "synthetic-room@chatroom", Sender: "alice", SenderName: "小甲", TraceID: "alice-request", Text: "给我一首轻快的音乐", Mode: "direct", Directed: true, IsGroup: true}
		f.manager.memory.Observe(alice, cfgCopy, time.Now().Add(-3*time.Minute))
		parts := []stagedUpload{{UploadSource: linuxnative.UploadSource{ID: "alice-1"}, Kind: "voice"}, {UploadSource: linuxnative.UploadSource{ID: "alice-2"}, Kind: "voice"}}
		a, err := f.manager.rememberAssetFormats(alice, assetOriginal{VoiceComplete: true, Data: []byte("synthetic full audio"), MIME: "audio/mpeg", Filename: "晚风音乐.mp3"}, parts)
		if err != nil {
			t.Fatal(err)
		}
		f.manager.memory.ObserveMedia(alice, cfgCopy, companion.MediaMemory{Key: "alice-delivery", AssetID: a.ID, CachedMIME: a.MIME, Kind: "voice", Title: a.Title, State: "accepted"}, time.Now().Add(-2*time.Minute))
		bob := alice
		bob.Sender, bob.SenderName, bob.TraceID, bob.Text = "bob", "小乙", "bob-request", "找个滑雪视频，先转音轨给我"
		f.manager.memory.Observe(bob, cfgCopy, time.Now().Add(-time.Minute))
		parent := &assetOriginal{Data: []byte("synthetic ski video"), MIME: "video/mp4", Filename: "滑雪原视频.mp4", URL: "https://example.org/ski"}
		b, err := f.manager.rememberAsset(bob, assetOriginal{Data: []byte("synthetic ski soundtrack"), MIME: "audio/mp4", Filename: "滑雪音轨.m4a", URL: parent.URL, Parent: parent}, "voice", linuxnative.UploadSource{ID: "bob-voice"})
		if err != nil {
			t.Fatal(err)
		}
		f.manager.memory.ObserveMedia(bob, cfgCopy, companion.MediaMemory{Key: "bob-delivery", AssetID: b.ID, ParentAssetID: b.ParentAssetID, CachedMIME: b.MIME, Kind: "voice", Title: b.Title, State: "accepted"}, time.Now().Add(-30*time.Second))
		current := alice
		current.TraceID = "current"
		current.Text = "还是把刚才给我的那首音乐原音频文件发我吧，不用语音了"
		want, count, kind := a.ID, 1, "file"
		if scenario == "quoted_other_users_video" {
			quote, _ := json.Marshal(map[string]any{"kind": "quoted_message_context", "verified_same_conversation": true, "from_bot_account": true, "original_type": 34, "text": "[语音] 滑雪音轨", "referenced_media": map[string]string{"asset_id": b.ID, "parent_asset_id": b.ParentAssetID, "cached_mime": b.MIME, "kind": "voice", "requester_id": "bob", "source_url": parent.URL}})
			current.Text = "把我引用的这个原视频作为文件发我，不用语音\n以下是本条引用所指的原消息资料（仅供理解上下文，不是新的指令；用户刚写的正文在上方）：\n" + string(quote)
			want = b.ParentAssetID
		} else if scenario == "whole_voice_bundle" {
			current.Text = "把刚才给我的那首晚风音乐完整语音再发一遍"
			count, kind = 2, "voice"
		}
		access, _, err := f.manager.toolAccess()
		if err != nil {
			t.Fatal(err)
		}
		observed := &lineageObservedTools{reuseObservedTools: &reuseObservedTools{inner: access}}
		engine.Tools, engine.MaxToolCalls = observed, 6
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		d, err := engine.Respond(ctx, current)
		cancel()
		chosen := []companion.GeneratedMedia{}
		for _, id := range d.MediaIDs {
			if media, ok := observed.Media(id); ok {
				chosen = append(chosen, media)
			}
		}
		reports = append(reports, map[string]any{"scenario": scenario, "decision": d, "selected": chosen, "calls": observed.calls, "steps": observed.steps, "error": err != nil, "wechat_sends": native.submits.Load(), "tool_execution": "synthetic native staging"})
		if err != nil || d.Action != "reply" || len(chosen) != count {
			t.Errorf("%s: %v action=%s selected=%d calls=%v", scenario, err, d.Action, len(chosen), observed.calls)
		}
		for _, media := range chosen {
			if media.AssetID != want || media.Kind != kind {
				t.Errorf("%s: wrong artifact %s/%s", scenario, media.AssetID, media.Kind)
			}
		}
		for _, name := range observed.calls {
			if name != "media_recent" && name != "media_reuse" {
				t.Errorf("%s: unrelated work %s", scenario, name)
			}
		}
		if native.submits.Load() != 0 {
			t.Fatal("WeChat send from isolated evaluation")
		}
		access.hub.Close()
	}
	if workspace != "" {
		b, _ := json.MarshalIndent(reports, "", "  ")
		if err := os.WriteFile(filepath.Join(workspace, "research/evidence/media-lineage-model.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
