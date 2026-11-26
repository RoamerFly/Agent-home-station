package admin

import (
	"context"
	"encoding/json"
	"testing"

	"wechat-bot/internal/companion"
)

func TestStationComfyVideoReferenceFromRealRequestShape(t *testing.T) {
	request := "生成视频：素材：你自己，锁定角色发型、服装、兽耳、尾巴与 Q 版比例，9:16，15s，720p"
	if !stationComfyOwnImageRequested(request) {
		t.Fatal("own character reference was missed")
	}
	if got := stationComfyImageInput("h3-image-to-video"); got != "input_image_1" {
		t.Fatalf("image input=%q", got)
	}
	upload := companion.Input{Thread: "103@chatroom", IncomingMedia: []companion.IncomingMediaRef{{Kind: "image", Source: companion.SourceRef{Thread: "103@chatroom", MessageID: "uploaded-mini-image"}}}}
	if got := stationComfyCurrentImage(upload); got != "uploaded-mini-image" {
		t.Fatalf("latest uploaded reference=%q", got)
	}
	correction := companion.Input{Thread: "103@chatroom", Sender: "owner", Text: "只用这张参考图", MessageTime: "2026-11-26T08:48:30+08:00", UnreadMessages: []companion.ConversationEvent{
		{Sender: "owner", Source: &companion.SourceRef{Thread: "103@chatroom", MessageID: "old-image", MessageType: 3, OccurredAt: "2026-11-26T08:40:00+08:00"}},
		{Sender: "owner", Source: &companion.SourceRef{Thread: "103@chatroom", MessageID: "uploaded-mini-image", MessageType: 3, OccurredAt: "2026-11-26T08:48:00+08:00"}},
		{Sender: "owner", Source: &companion.SourceRef{Thread: "103@chatroom", MessageID: "later-owner-image", MessageType: 3, OccurredAt: "2026-11-26T08:48:45+08:00"}},
		{Sender: "other", Source: &companion.SourceRef{Thread: "103@chatroom", MessageID: "other-image", MessageType: 3, OccurredAt: "2026-11-26T08:49:00+08:00"}},
	}}
	if got := stationComfyCurrentImage(correction); got != "uploaded-mini-image" {
		t.Fatalf("explicit correction did not choose latest same-sender upload: %q", got)
	}
}

func TestReferenceCorrectionCannotUseTextOnlyComfyWorkflow(t *testing.T) {
	_, p := trustedAgent(t)
	p.trace.Text = "只用这张参考图"
	for _, raw := range []string{
		`{"action":"run","workflow_id":"h3-text-to-video"}`,
		`{"action":"run","workflow_id":"h3-text-to-video","source_message_id":"uploaded-mini-image"}`,
	} {
		if _, err := p.callStationComfy(context.Background(), json.RawMessage(raw)); err == nil || err.Error() != "station_comfy_image_workflow_required" {
			t.Fatalf("text-only workflow silently ignored reference: %s %v", raw, err)
		}
	}
}
