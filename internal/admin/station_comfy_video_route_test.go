package admin

import (
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
}
