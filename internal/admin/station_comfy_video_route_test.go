package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

func TestCorrectedVideoRequiresExactUploadedMessageForComfyRun(t *testing.T) {
	f, p := trustedAgent(t)
	task := agentTask{
		ID: "corrected-video", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", State: "waiting",
		CorrectionAt: time.Now().UTC(), CorrectionRequiredMediaKind: "video", CorrectionReferenceImageID: "uploaded-mini-image",
	}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	p.trace.TaskID = task.ID
	for _, raw := range []string{
		`{"action":"run","workflow_id":"h3-image-reference-to-video"}`,
		`{"action":"run","workflow_id":"h3-image-reference-to-video","own_mini":true}`,
		`{"action":"run","workflow_id":"h3-image-reference-to-video","source_path":"C:\\mini.jpg"}`,
		`{"action":"run","workflow_id":"h3-image-reference-to-video","source_message_id":"old-image"}`,
	} {
		if _, err := p.callStationComfy(context.Background(), json.RawMessage(raw)); err == nil || err.Error() != "纠错视频请显式传新上传图片的 source_message_id；不能用 own_mini 或 source_path" {
			t.Fatalf("uncorrected source reached Comfy: %s %v", raw, err)
		}
	}
	if _, err := p.callStationComfy(context.Background(), json.RawMessage(`{"action":"run","workflow_id":"h3-image-reference-to-video","source_message_id":"uploaded-mini-image"}`)); err == nil || err.Error() == "纠错视频请显式传新上传图片的 source_message_id；不能用 own_mini 或 source_path" {
		t.Fatalf("exact uploaded message was rejected by corrected-source guard: %v", err)
	}
}
