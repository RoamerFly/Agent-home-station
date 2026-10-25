package companion

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/mcptools"
)

type promiseMusicModel struct {
	step  int
	never bool
}

func (m *promiseMusicModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}
func (m *promiseMusicModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	m.step++
	found := false
	for _, tool := range tools {
		found = found || tool.Name == "music_local_start"
	}
	if !found {
		panic("song schema hidden")
	}
	if m.step == 1 || m.never {
		return `{"action":"reply","messages":["我整一首忧郁的原创，稍等我一小会儿。"]}`, nil
	}
	if m.step == 2 {
		return `{"action":"tool","tool":"music_local_start","arguments":{"style":"melancholic","lyrics":"[verse]雨落下来"}}`, nil
	}
	return `{"action":"reply","messages":["已经开始制作啦，做好后给你。"]}`, nil
}

type promiseMusicTools struct{ starts int }

func (*promiseMusicTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "music_local_start", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (p *promiseMusicTools) Call(context.Context, string, json.RawMessage) (string, error) {
	p.starts++
	return `{"job_id":"real-job","automatic_followup":true}`, nil
}
func TestMusicPromiseMustActuallyStartTaskEvenForShortStyleConfirmation(t *testing.T) {
	for _, never := range []bool{false, true} {
		model := &promiseMusicModel{never: never}
		tools := &promiseMusicTools{}
		engine, _ := NewPreview(enabledConfig(), model, "global")
		engine.Tools = tools
		engine.MaxToolCalls = 4
		d, err := engine.Respond(context.Background(), Input{Mode: "direct", Text: "可以，忧郁风格吧", Preview: true})
		if err != nil || d.Action != "reply" {
			t.Fatal(err, d)
		}
		if !never && tools.starts != 1 {
			t.Fatal("empty promise accepted", d)
		}
		if never && (claimsDeferredMusic(d.Messages) || d.Reason != "music_execution_not_started") {
			t.Fatal("unfulfilled promise leaked", d)
		}
	}
}

type aliasMusicTools struct{ promiseMusicTools }

type semanticPromiseModel struct{ promiseMusicModel }

func (m *semanticPromiseModel) VerifyMusicPromise(context.Context, string, string) (bool, error) {
	return true, nil
}
func (m *semanticPromiseModel) ChatFunctions(ctx context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	if m.step == 0 {
		m.step++
		return `{"action":"reply","messages":["好呀，下班路上听的歌这就安排～ 我写首轻快带点松弛的中文原创，大概两分钟 🎵"]}`, nil
	}
	return m.promiseMusicModel.ChatFunctions(ctx, system, turns, tools)
}
func TestSemanticMusicPromiseRepairsExactReportedEmptyPromise(t *testing.T) {
	model := &semanticPromiseModel{}
	tools := &promiseMusicTools{}
	engine, _ := NewPreview(enabledConfig(), model, "global")
	engine.Tools = tools
	engine.MaxToolCalls = 4
	d, err := engine.Respond(context.Background(), Input{Mode: "direct", Text: "给我唱一首两分钟左右的，适合下班听的中文歌曲", Preview: true})
	if err != nil || tools.starts != 1 || d.Action != "reply" {
		t.Fatal("semantic repair did not execute", err, tools.starts, d)
	}
}

type bothMusicTools struct{ promiseMusicTools }

func (*bothMusicTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "music_local_status", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (*bothMusicTools) Call(context.Context, string, json.RawMessage) (string, error) {
	return `{"media":[{"id":"voice-part","kind":"voice"},{"id":"original-file","kind":"file"}],"required_media_ids":["voice-part","original-file"]}`, nil
}
func (*bothMusicTools) Media(id string) (GeneratedMedia, bool) {
	kind := "voice"
	if id == "original-file" {
		kind = "file"
	}
	return GeneratedMedia{ID: id, Kind: kind}, id == "voice-part" || id == "original-file"
}

type bothMusicModel struct{ step int }

func (*bothMusicModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}
func (m *bothMusicModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	m.step++
	if m.step == 1 {
		return `{"action":"tool","tool":"music_local_status","arguments":{"job_id":"done"}}`, nil
	}
	return `{"action":"reply","messages":["做好啦～"],"media_ids":["original-file"]}`, nil
}
func TestBothMusicManifestKeepsVoiceThenFileWhenModelOmitsOne(t *testing.T) {
	cfg := enabledConfig()
	cfg.Permissions.SendVoice = true
	cfg.Permissions.SendFile = true
	e, _ := NewPreview(cfg, &bothMusicModel{}, "global")
	e.Tools = &bothMusicTools{}
	e.MaxToolCalls = 4
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "唱给我听并发送文件给我", Preview: true})
	if err != nil || len(d.MediaIDs) != 2 || d.MediaIDs[0] != "voice-part" || d.MediaIDs[1] != "original-file" {
		t.Fatal("both delivery lost or reordered", err, d)
	}
}

func (*aliasMusicTools) Media(id string) (GeneratedMedia, bool) {
	return GeneratedMedia{ID: "source-one", Kind: "voice"}, id == "asset-one" || id == "source-one"
}
func TestPreparedMediaAliasNormalizesToActualAttachmentID(t *testing.T) {
	cfg := enabledConfig()
	cfg.Permissions.SendVoice = true
	model := &fakeModel{answer: `{"action":"reply","messages":["做好啦"],"media_ids":["asset-one"]}`}
	engine, _ := NewPreview(cfg, model, "global")
	engine.Tools = &aliasMusicTools{}
	engine.MaxToolCalls = 4
	d, err := engine.Respond(context.Background(), Input{Mode: "direct", Text: "可以", Preview: true})
	if err != nil || d.Action != "reply" || len(d.MediaIDs) != 1 || d.MediaIDs[0] != "source-one" {
		t.Fatal("asset alias leaked into delivery", err, d)
	}
}
