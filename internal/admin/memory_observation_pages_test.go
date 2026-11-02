package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestOriginalObservationPagesPreserveWordsVersionAndScopeWithoutEnvelopeDuplication(t *testing.T) {
	f, p := globalSearchFixture(t)
	body := "[微信视频实际内容观察]\n[画面观察：6帧]\n合成画面说明不应重复注入音轨页。\n[实际音轨转写：0–120秒]\n" + strings.Repeat("合成视频音轨中的完整原话。", 500)
	empty := ""
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "member", TraceID: "synthetic-original-video", Text: "收到视频", OriginalText: &empty, IncomingMedia: []companion.IncomingMediaRef{{Kind: "video", UploaderID: "member", Observation: body, ObservationState: "observed", ObservationVersion: 1}}}
	in.Source = &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: in.TraceID, MessageType: 43}
	if err := f.manager.memory.Observe(in, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	source, ok := f.manager.memory.SourceForInput(in)
	if !ok {
		t.Fatal("source missing")
	}
	args := map[string]any{"thread_id": in.Thread, "id": source.EventID, "event": true, "part": "observation", "length": 999}
	joined := ""
	offset := 0
	for {
		args["offset"] = offset
		args["expected_revision"] = source.Revision
		raw, _ := json.Marshal(args)
		text, err := p.Call(t.Context(), "memory_read", raw)
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			Text  string                `json:"text"`
			Next  int                   `json:"next_offset"`
			More  bool                  `json:"has_more"`
			Event companion.MemoryEvent `json:"event"`
			Parts []any                 `json:"available_parts"`
		}
		if json.Unmarshal([]byte(text), &value) != nil || value.Next <= offset || len(value.Parts) != 3 {
			t.Fatal(text)
		}
		if len(value.Event.IncomingMedia) != 1 || value.Event.IncomingMedia[0].Observation != "" || value.Event.Text != "" {
			t.Fatal("body duplicated outside requested page")
		}
		joined += value.Text
		offset = value.Next
		if !value.More {
			break
		}
	}
	if joined != body {
		t.Fatal("pagination changed raw observation")
	}
	args["offset"] = 0
	args["part"] = "transcript"
	raw, _ := json.Marshal(args)
	transcript, err := p.Call(t.Context(), "memory_read", raw)
	if err != nil || strings.Contains(transcript, "合成画面说明") || !strings.Contains(transcript, "合成视频音轨") {
		t.Fatal("transcript-only page duplicated visuals or lost audio", err, transcript)
	}
	args["offset"] = 0
	args["expected_revision"] = source.Revision + 1
	raw, _ = json.Marshal(args)
	if _, err := p.Call(t.Context(), "memory_read", raw); err == nil {
		t.Fatal("old revision ignored")
	}
	q, _ := json.Marshal(map[string]any{"thread_id": in.Thread, "originals": true, "media_kind": "video_or_share", "metadata_only": true})
	text, err := p.Call(t.Context(), "memory_search", q)
	if err != nil || strings.Contains(text, body[:60]) || !strings.Contains(text, "observation_bytes") {
		t.Fatal("metadata query copied observation", err, text)
	}
	settings := f.manager.agent.config()
	settings.CrossConversation = false
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Call(t.Context(), "memory_read", raw); err == nil {
		t.Fatal("revocation ignored")
	}
}
