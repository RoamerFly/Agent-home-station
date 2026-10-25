package bot

import (
	"context"
	"encoding/json"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/sending"
)

type characterModel struct{}

type invalidDecisionModel struct{ calls int }

func (m *invalidDecisionModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	return `{"action":"reply","messages":`, nil
}
func TestDirectInvalidDecisionReportsFailureInsteadOfSilentDrop(t *testing.T) {
	c := companion.Defaults()
	c.Enabled = true
	c.ReplyMinMS = 0
	c.ReplyMaxMS = 0
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	store, e := companion.NewStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	model := &invalidDecisionModel{}
	engine, e := companion.New(c, store, model, "role")
	if e != nil {
		t.Fatal(e)
	}
	sender := &fakeSender{}
	records := []Record{}
	w, e := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"filehelper"}, Record: func(r Record) { records = append(records, r) }}, &fakeSource{}, engine, sender)
	if e != nil {
		t.Fatal(e)
	}
	w.respondCharacter(context.Background(), engine, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "video-request", Text: "找个动画视频发给我", Mode: "direct"})
	if model.calls != 3 || sender.calls != 1 {
		t.Fatal("request silently dropped or unbounded correction", model.calls, sender.calls)
	}
	failed := false
	for _, r := range records {
		failed = failed || r.Event == "model_decision_failed"
	}
	if !failed {
		t.Fatal("missing actual failure record")
	}
}

func (characterModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return `{"action":"reply","messages":[],"image_id":"approved-image"}`, nil
}

type generatedModel struct{}

func (generatedModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return `{"action":"reply","messages":[],"media_ids":["generated-voice"]}`, nil
}

type generatedTools struct{}

func (generatedTools) List(context.Context) ([]mcptools.Tool, error) { return nil, nil }
func (generatedTools) Call(context.Context, string, json.RawMessage) (string, error) {
	panic("unexpected tool call")
}
func (generatedTools) Media(id string) (companion.GeneratedMedia, bool) {
	return companion.GeneratedMedia{ID: id, Kind: "voice"}, id == "generated-voice"
}

type generatedSender struct {
	fakeSender
	media int
}

func (s *generatedSender) SendMedia(_ context.Context, thread, kind, id string) (linuxnative.ToolStatus, error) {
	if thread != "filehelper" || kind != "voice" || id != "generated-voice" {
		panic("wrong generated media")
	}
	s.media++
	return linuxnative.ToolStatus{}, sending.ErrOutcomeUnknown
}
func TestGeneratedVoiceOnlyAndUnknownNotRetried(t *testing.T) {
	config := companion.Defaults()
	config.Enabled = true
	config.ReplyMinMS = 0
	config.ReplyMaxMS = 0
	store, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(config, store, generatedModel{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = generatedTools{}
	sender := &generatedSender{}
	worker, err := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"filehelper"}}, &fakeSource{}, engine, sender)
	if err != nil {
		t.Fatal(err)
	}
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Text: "sing", Mode: "direct"}
	worker.respondCharacter(context.Background(), engine, input)
	if sender.media != 1 || sender.calls != 0 {
		t.Fatal("unexpected media sends", sender.media, sender.calls)
	}
	engine.Config.Permissions.SendVoice = false
	worker.respondCharacter(context.Background(), engine, input)
	if sender.media != 1 {
		t.Fatal("voice permission revoke ignored")
	}
}

type characterSender struct {
	fakeSender
	images   int
	imageErr error
}

func (s *characterSender) SendImage(_ context.Context, recipient, source string) (linuxnative.ToolStatus, error) {
	s.images++
	if recipient != "filehelper" || source != "approved-image" {
		panic("wrong image destination/source")
	}
	return linuxnative.ToolStatus{}, s.imageErr
}
func TestCharacterImageUnknownIsNotRetried(t *testing.T) {
	c := companion.Defaults()
	c.Enabled = true
	c.Permissions.SendImage = true
	c.ReplyMinMS = 0
	c.ReplyMaxMS = 0
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	store, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(c, store, characterModel{}, "global")
	if err != nil {
		t.Fatal(err)
	}
	engine.Images = []companion.ImageAsset{{ID: "approved-image", Title: "hello", Description: "wave"}}
	sender := &characterSender{imageErr: sending.ErrOutcomeUnknown}
	worker, err := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"filehelper"}}, &fakeSource{}, engine, sender)
	if err != nil {
		t.Fatal(err)
	}
	worker.respondCharacter(context.Background(), engine, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Text: "image", Mode: "direct"})
	if sender.images != 1 || sender.calls != 0 {
		t.Fatal("image-only action resent or sent extra text", sender.images, sender.calls)
	}
	engine.Config.Permissions.SendImage = false
	worker.respondCharacter(context.Background(), engine, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Text: "image", Mode: "direct"})
	if sender.images != 1 {
		t.Fatal("revoked image permission ignored")
	}
}
