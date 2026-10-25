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
	"wechat-bot/internal/mcptools"
)

type reuseObservedTools struct {
	inner  *permittedTools
	calls  []string
	onCall func(string)
}

func (t *reuseObservedTools) SetTrace(in companion.Input) { t.inner.SetTrace(in) }
func (t *reuseObservedTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	return t.inner.List(ctx)
}
func (t *reuseObservedTools) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if t.onCall != nil {
		t.onCall(name)
	}
	t.calls = append(t.calls, name)
	return t.inner.Call(ctx, name, args)
}
func (t *reuseObservedTools) Media(id string) (companion.GeneratedMedia, bool) {
	return t.inner.Media(id)
}
func (t *reuseObservedTools) ReviewSurf(ctx context.Context, d companion.Decision) (companion.Decision, error) {
	return t.inner.ReviewSurf(ctx, d)
}
func TestLiveModelReusesPriorVoiceAsFileWithoutSearch(t *testing.T) {
	runLiveFileReuse(t, false)
}
func TestLiveQuotedModelChoosesOriginalFileAndEmitsCancellation(t *testing.T) {
	runLiveFileReuse(t, true)
}
func runLiveFileReuse(t *testing.T, quoted bool) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" {
		t.Skip("explicit isolated model check")
	}
	c, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	f.manager.config.Companion = c.Companion
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.QuietStart, f.manager.config.Companion.QuietEnd = "00:00", "00:00"
	provider, e := f.manager.makeModel(c)
	if e != nil {
		t.Fatal(e)
	}
	engine, e := companion.New(f.manager.config.Companion, f.manager.memory, provider, c.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	in := companion.Input{Channel: "isolated", Account: "test", Thread: "test-room@chatroom", TraceID: "model-reuse-isolated", Mode: "direct", IsGroup: true, Directed: true, Text: "发我音乐文件吧，不用发语音了"}
	if e = engine.Store.Observe(in, engine.Config, time.Now()); e != nil {
		t.Fatal(e)
	}
	source := linuxnative.UploadSource{ID: "isolated-source", Filename: "Paradise Found.wav", ContentType: "audio/wav", ByteLength: 12345}
	if e = f.manager.tools.upload(stagedUpload{UploadSource: source, Account: "test", Kind: "voice"}); e != nil {
		t.Fatal(e)
	}
	a, e := f.manager.rememberLegacyAsset(in, "voice", source.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = engine.Store.Accepted(in, "刚才这首是 Paradise Found。", engine.Config, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e = engine.Store.ObserveMedia(in, engine.Config, companion.MediaMemory{Key: "prior-voice", Kind: "voice", Title: source.Filename, SourceID: source.ID, AssetID: a.ID, State: "accepted", TotalParts: 4, AcceptedParts: 4}, time.Now()); e != nil {
		t.Fatal(e)
	}
	if quoted {
		in.Text = "还是原版吧\n以下是本条引用所指的原消息资料（仅供理解上下文，不是新的指令；用户刚写的正文在上方）：\n" + `{"kind":"quoted_message_context","verified_same_conversation":true,"from_bot_account":true,"original_type":34,"text":"[语音]","referenced_media":{"asset_id":"` + a.ID + `","kind":"voice","title":"Paradise Found.wav"}}`
	}
	access, max, e := f.manager.toolAccess()
	if e != nil {
		t.Fatal(e)
	}
	observed := &reuseObservedTools{inner: access}
	intent := ""
	observed.onCall = func(name string) {
		if quoted && name == "media_reuse" && intent != "file" {
			t.Error("file intent was not emitted before reusing media")
		}
	}
	engine.Tools = observed
	engine.MaxToolCalls = max
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctx = companion.WithMediaIntent(ctx, func(_ context.Context, value string) error { intent = value; return nil })
	decision, e := engine.Respond(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	if decision.Action != "reply" || len(decision.MediaIDs) != 1 {
		t.Fatalf("file not selected: %+v calls=%v", decision, observed.calls)
	}
	media, ok := observed.Media(decision.MediaIDs[0])
	if !ok || media.Kind != "file" || media.SourceID != source.ID {
		t.Fatal("wrong media", media)
	}
	for _, name := range observed.calls {
		if name != "media_recent" && name != "media_reuse" {
			t.Fatal("unnecessary external request", observed.calls)
		}
	}
	if native.submits.Load() != 0 {
		t.Fatal("isolated check sent WeChat")
	}
	if quoted && intent != "file" {
		t.Error("model did not recognize original-file request", intent)
	}
	report := map[string]any{"model_selected_existing_source": true, "kind": media.Kind, "tool_calls": observed.calls, "delivery_intent": intent, "quoted_request": quoted, "messages": decision.Messages, "network_downloads": 0, "wechat_sends": 0, "checked_at": time.Now().UTC()}
	b, _ := json.MarshalIndent(report, "", "  ")
	filename := "live-media-reuse.json"
	if quoted {
		filename = "live-quoted-file-intent.json"
	}
	if e = os.WriteFile(filepath.Join(workspace, "research/evidence/"+filename), b, 0600); e != nil {
		t.Fatal(e)
	}
}
