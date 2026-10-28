package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestCrossConversationVideoUsesOriginalCacheAndExplicitDestinationScope(t *testing.T) {
	f, p, n := forwardFixture(t)
	f.manager.config.Behavior.ReadVideos = true
	n.message = linuxnative.Message{ID: "cached-video", AccountID: "test", ThreadID: "source@chatroom", Type: 43}
	trace := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", TraceID: n.message.ID}
	a, err := f.manager.rememberAssetFormats(trace, assetOriginal{Data: inspectionFixture(t), MIME: "video/mp4", Filename: "synthetic.mp4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []map[string]string{{"message_id": n.message.ID}, {"asset_id": a.ID, "source_thread_id": "source@chatroom"}} {
		source["action"] = "info"
		raw, _ := json.Marshal(source)
		result, err := p.Call(context.Background(), "video_inspect", raw)
		if err != nil || !strings.Contains(result, `"source_thread_id":"source@chatroom"`) || !strings.Contains(result, `"origin_thread_id":"wxid_owner"`) || n.downloads != 0 {
			t.Fatal(result, err, n.downloads)
		}
	}
}

func TestOwnerCanInspectAnotherAuthorizedConversationWithoutChangingOrigin(t *testing.T) {
	f, p, n := forwardFixture(t)
	f.manager.config.Behavior.ReadVideos = true
	origin := p.trace.Thread
	n.message = linuxnative.Message{ID: "video-message", AccountID: "test", ThreadID: "source@chatroom", Type: 43}
	in := videoInspectionInput{Message: n.message.ID}
	data, _, err := p.inspectionSource(context.Background(), &in)
	if err != nil || len(data) == 0 || in.SourceThread != "source@chatroom" || p.trace.Thread != origin {
		t.Fatal(in, err)
	}
	// Explicit source is binding; it cannot be silently replaced by metadata.
	in.SourceThread = origin
	if _, _, err = p.inspectionSource(context.Background(), &in); err == nil {
		t.Fatal("explicit source ignored")
	}
	in.SourceThread = ""
	n.message.AccountID = "another-account"
	if _, _, err = p.inspectionSource(context.Background(), &in); err == nil {
		t.Fatal("another account exposed")
	}
	n.message.AccountID = "test"
	p.trace.Sender = "ordinary-member"
	in.SourceThread = ""
	if _, _, err = p.inspectionSource(context.Background(), &in); err == nil {
		t.Fatal("member cross-read")
	}
	p.trace.Sender = "wxid_owner"
	cfg := f.manager.agent.config()
	cfg.CrossConversation = false
	f.manager.agent.save(cfg)
	in.SourceThread = ""
	if _, _, err = p.inspectionSource(context.Background(), &in); err == nil {
		t.Fatal("disabled cross-read")
	}
}

func TestOwnerDiagnosticsAreScopedPaginatedAndExcludeRawSecrets(t *testing.T) {
	f, p, _ := forwardFixture(t)
	f.manager.record(bot.Record{Event: "video_audio_no_words", ThreadID: "source@chatroom", MessageID: "target", Status: "no_recognizable_speech", ToolCode: "ASR_RESPONSE_HAVE_NO_WORDS", ToolHTTPStatus: 400, Content: "synthetic private content", Reason: "synthetic private credential", Origin: "speech"})
	result, err := p.Call(context.Background(), "agent_diagnostics", json.RawMessage(`{"thread_id":"source@chatroom","message_id":"target","limit":1}`))
	if err != nil || !strings.Contains(result, "ASR_RESPONSE_HAVE_NO_WORDS") || strings.Contains(result, "private") {
		t.Fatal(result, err)
	}
	p.trace.Account = "different-account"
	if _, err = p.Call(context.Background(), "agent_diagnostics", json.RawMessage(`{}`)); err == nil {
		t.Fatal("different account")
	}
	p.trace.Account = "test"
	p.trace.Sender = "ordinary-member"
	if _, err = p.Call(context.Background(), "agent_diagnostics", json.RawMessage(`{}`)); err == nil {
		t.Fatal("member diagnostics")
	}
}
