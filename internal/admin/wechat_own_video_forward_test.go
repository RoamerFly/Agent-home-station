package admin

import (
	"context"
	"encoding/json"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type outgoingVideoForwardNative struct {
	*forwardNative
	video []byte
}

func (*outgoingVideoForwardNative) Capabilities(context.Context) (linuxnative.Capabilities, error) {
	return linuxnative.Capabilities{MediaKinds: []string{"video"}, MediaSendMaxBytes: map[string]int64{"video": 50 << 20}}, nil
}

func (n *outgoingVideoForwardNative) IncomingMedia(_ context.Context, _ string, _ int64) ([]byte, string, error) {
	n.downloads++
	return n.video, "video/mp4", nil
}

func TestForwardOwnVideoMessageUsesExactMessageInsteadOfRecentUnrelatedAsset(t *testing.T) {
	f, p, base := forwardFixture(t)
	n := &outgoingVideoForwardNative{forwardNative: base, video: inspectionFixture(t)}
	f.manager.native = n
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVideo = true
	f.manager.mu.Unlock()
	old := linuxnative.Message{ID: "older-video", AccountID: "test", ThreadID: "source@chatroom", SenderID: "source-member", Type: 43, CreatedAt: "2026-10-29T12:00:00Z", ServerID: "older-server"}
	oldInput := companion.Input{Channel: "wechat", Account: old.AccountID, Thread: old.ThreadID, Sender: old.SenderID, TraceID: old.ID, MessageTime: old.CreatedAt}
	oldAsset, err := f.manager.rememberAssetFormats(oldInput, assetOriginal{InboundMessage: &old, Data: []byte("older video bytes"), MIME: "video/mp4", Filename: "old.mp4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.message = linuxnative.Message{ID: "just-sent-video", AccountID: "test", ThreadID: "source@chatroom", SenderID: "test", Direction: "outgoing", Type: 43, CreatedAt: "2026-10-30T12:00:00Z", ServerID: "new-server"}
	raw := json.RawMessage(`{"source_thread_id":"source@chatroom","target_thread_id":"wxid_owner","message_id":"just-sent-video","kind":"video","request_key":"forward-own-video"}`)
	result, err := p.Call(context.Background(), "wechat_send_media", raw)
	if err != nil {
		t.Fatal(err)
	}
	seeds := f.manager.takeMediaSeeds()
	if len(seeds) != 1 || seeds[0].Media.AssetID == oldAsset.ID || seeds[0].Media.Kind != "video" || seeds[0].Input.Thread != "wxid_owner" || n.downloads != 1 || string(n.uploaded) != string(n.video) {
		t.Fatalf("wrong video forwarded: result=%s seeds=%+v downloads=%d", result, seeds, n.downloads)
	}
}
