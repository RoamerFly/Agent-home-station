package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestAgentReopensCompletedVideoForSameSenderReferenceCorrection(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.native = &taskCorrectionNative{fakeNative: f.native, messages: map[string]linuxnative.Message{
		"original":   {ID: "original", AccountID: "test", ThreadID: "wxid_owner", SenderID: "wxid_owner", Direction: "incoming", Type: 1, Body: "生成十五秒视频"},
		"new-image":  {ID: "new-image", AccountID: "test", ThreadID: "wxid_owner", SenderID: "wxid_owner", Direction: "incoming", Type: 3},
		"correction": {ID: "correction", AccountID: "test", ThreadID: "wxid_owner", SenderID: "wxid_owner", Direction: "incoming", Type: 1, Body: "只用这张参考图"},
	}}
	old := agentTask{ID: "original-video-task", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner",
		OriginTrace: "original", SourceMessageIDs: []string{"original"}, RequestedText: "生成十五秒视频",
		Title: "原视频", State: "completed", WorkCompleted: true, Updated: time.Now().UTC(),
		Delivery: &bot.TaskDelivery{Trace: "old-trace", State: "accepted", Expected: 1, Accepted: 1},
		Actions:  []taskAction{{Tool: "station_comfy", Arguments: `{"action":"run"}`, Result: `{"job_id":"old-job"}`, At: time.Now().Add(-time.Minute)}},
	}
	if err := f.manager.agent.task(old); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.agent.retireSettled(); err != nil {
		t.Fatal(err)
	}
	correction := "只用这张参考图"
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner",
		TraceID: "correction", OriginalText: &correction, Mode: "direct"})
	p.recordCrossSourceMessages("wxid_owner", `{"items":[{"id":"original"},{"id":"new-image"},{"id":"correction"}]}`)
	args := json.RawMessage(`{"origin_message_id":"original","source_message_ids":["new-image","correction"],"corrected_request":true,"checkpoint":"沿用原请求，只用新图重新生成视频","state":"pending"}`)
	raw, err := p.Call(t.Context(), "agent_task", args)
	var got agentTask
	if err != nil || json.Unmarshal([]byte(raw), &got) != nil || got.ID != old.ID || got.State != "pending" || got.CorrectionReferenceImageID != "new-image" ||
		!strings.Contains(got.Checkpoint, "old-job") || got.Delivery != nil || got.CorrectionAt.IsZero() {
		t.Fatalf("explicit correction did not reopen original task: %s %v", raw, err)
	}
	if len(f.manager.agent.active("test", "wxid_owner")) != 1 {
		t.Fatal("correction created another task")
	}
}
