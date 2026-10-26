package companion

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/mcptools"
)

type promiseMusicTools struct{ starts int }

func (*promiseMusicTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "music_local_start", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (p *promiseMusicTools) Call(context.Context, string, json.RawMessage) (string, error) {
	p.starts++
	return `{"job_id":"real-job","automatic_followup":true}`, nil
}

type aliasMusicTools struct{ promiseMusicTools }

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
