package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
	"wechat-bot/internal/mcptools"
)

func TestMediaRequestAssociationSurvivesRecoveryWithoutLosingOrigin(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	c := enabledConfig()
	c.Permissions.Remember = true
	in := Input{Channel: "wechat", Account: "test", Thread: "room", Sender: "alice", SenderName: "小甲", TraceID: "delivery", OriginTraceID: "original", TaskID: "task-1"}
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	media := MediaMemory{Key: "first-part", AssetID: "audio", ParentAssetID: "video", CachedMIME: "audio/mp4", Kind: "voice", State: "running", AcceptedParts: 1, TotalParts: 3}
	if err := s.ObserveMedia(in, c, media, time.Now()); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := recentMediaFocus(s.Context(in, c, time.Now()))
	if len(got) != 1 || got[0].RequesterID != "alice" || got[0].RequestTraceID != "original" || got[0].TaskID != "task-1" || got[0].ParentAssetID != "video" || got[0].CachedMIME != "audio/mp4" {
		t.Fatal(got)
	}
	media.State, media.ParentAssetID, media.CachedMIME = "accepted", "", ""
	in.Sender, in.SenderName = "", ""
	if err := s.ObserveMedia(in, c, media, time.Now()); err != nil {
		t.Fatal(err)
	}
	got = recentMediaFocus(s.Context(in, c, time.Now()))
	if len(got) != 1 || got[0].RequesterID != "alice" || got[0].ParentAssetID != "video" || got[0].CachedMIME != "audio/mp4" {
		t.Fatal(got)
	}
}

type semanticReuseModel struct {
	*sequenceModel
	allow  bool
	checks int
}

func (m *semanticReuseModel) VerifyResourceReuse(context.Context, string) (bool, error) {
	m.checks++
	return m.allow, nil
}

type sameResourceTools struct{ reused int }

func (*sameResourceTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "media_reuse"}}, nil
}
func (p *sameResourceTools) Call(context.Context, string, json.RawMessage) (string, error) {
	p.reused++
	return `{"media":[{"id":"same-audio","kind":"voice"}]}`, nil
}
func (*sameResourceTools) Media(id string) (GeneratedMedia, bool) {
	return GeneratedMedia{ID: id, Kind: "voice"}, id == "same-audio"
}
func TestSemanticReuseDecisionWinsOverLegacyFreshHint(t *testing.T) {
	m := &semanticReuseModel{sequenceModel: &sequenceModel{answers: []string{`{"action":"tool","tool":"media_reuse","arguments":{"asset_id":"same","kind":"voice"}}`, `{"action":"reply","messages":["这首给你"],"media_ids":["same-audio"]}`}}, allow: true}
	p := &sameResourceTools{}
	e, _ := NewPreview(enabledConfig(), m, "role")
	e.Tools, e.MaxToolCalls = p, 3
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "把刚才给我的那首音乐完整语音再发一遍", Preview: true})
	if err != nil || d.Action != "reply" || p.reused != 1 || m.checks != 1 {
		t.Fatal(d, err, p.reused, m.checks)
	}
}

func TestSemanticNewResourceDecisionStillRejectsUnrelatedCachedTrack(t *testing.T) {
	m := &semanticReuseModel{sequenceModel: &sequenceModel{answers: []string{
		`{"action":"tool","tool":"media_reuse","arguments":{"asset_id":"old-track","kind":"voice"}}`,
		`{"action":"tool","tool":"web_search","arguments":{"query":"Traveling Light song audio"}}`,
		`{"action":"tool","tool":"web_media_download","arguments":{"url":"https://example.org/audio.mp3","kind":"voice"}}`,
		`{"action":"reply","messages":["这首是 Traveling Light"],"media_ids":["voice-real"]}`,
	}}, allow: false}
	p := &newSongTools{demandedTools: &demandedTools{}}
	e, _ := NewPreview(enabledConfig(), m, "role")
	e.Tools, e.MaxToolCalls = p, 4
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "重新找一首，traveling light", Preview: true})
	if err != nil || len(d.MediaIDs) != 1 || m.checks != 1 || len(p.names) != 2 || p.names[0] != "web_search" || p.names[1] != "web_media_download" {
		t.Fatal(d, err, m.checks, p.names)
	}
}

func TestPreparedBundleDoesNotForceAReplyWhenModelChoosesSilence(t *testing.T) {
	m := &sequenceModel{answers: []string{`{"action":"tool","tool":"music_local_status","arguments":{"job_id":"done"}}`, `{"action":"silent"}`}}
	e, _ := NewPreview(enabledConfig(), m, "role")
	e.Tools, e.MaxToolCalls = &bothMusicTools{}, 3
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "这次先安静，不要发", Preview: true})
	if err != nil || d.Action != "silent" || len(d.MediaIDs) != 0 {
		t.Fatal(d, err)
	}
}

func TestBackfillMediaMetadataKeepsReceiptChronologyAndScope(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	c := enabledConfig()
	c.Permissions.Remember = true
	in := Input{Channel: "wechat", Account: "test", Thread: "one", TraceID: "request"}
	stamp := time.Now().Add(-time.Minute)
	s.Observe(in, c, stamp)
	s.ObserveMedia(in, c, MediaMemory{Key: "voice", AssetID: "asset", Kind: "voice", State: "accepted"}, stamp)
	id := ScopeID(in.Channel, in.Account, in.Thread)
	if err := s.BackfillMediaMetadata(map[string]map[string]MediaMemory{id: {"asset": {URL: "https://example.org/source", CachedMIME: "audio/mp4", ParentAssetID: "parent"}}, ScopeID(in.Channel, in.Account, "other"): {"asset": {URL: "https://example.org/wrong"}}}); err != nil {
		t.Fatal(err)
	}
	got := s.Context(in, c, time.Now()).Messages[1]
	if got.Media.URL != "https://example.org/source" || got.Media.CachedMIME != "audio/mp4" || got.Media.ParentAssetID != "parent" || got.Media.State != "accepted" || !got.Time.Equal(stamp) {
		t.Fatal(got)
	}
}

func TestMediaFocusDoesNotLetVoicePartsHideOtherParticipants(t *testing.T) {
	scope := Scope{Messages: []Turn{
		{Role: "user", Sender: "alice", SenderName: "小甲", TraceID: "alice-request"},
		{Role: "assistant", TraceID: "alice-request", Media: &MediaMemory{Key: "cat", AssetID: "cat-video", Kind: "video", State: "accepted"}},
		{Role: "user", Sender: "bob", SenderName: "小乙", TraceID: "bob-request"},
	}}
	for i := 0; i < 5; i++ {
		scope.Messages = append(scope.Messages, Turn{Role: "assistant", TraceID: "bob-request", Media: &MediaMemory{Key: fmt.Sprint(i), AssetID: "bob-song", Kind: "voice", State: "accepted"}})
	}
	scope.Messages = append(scope.Messages, Turn{Role: "assistant", TraceID: "not-sent", Media: &MediaMemory{Key: "pending", AssetID: "not-sent", State: "running"}})
	got := recentMediaFocus(scope)
	if len(got) != 2 || got[0].RequesterID != "bob" || got[1].RequesterID != "alice" || got[1].AssetID != "cat-video" {
		t.Fatal(got)
	}
}
