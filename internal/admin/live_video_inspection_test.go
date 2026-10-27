package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// This opt-in bills only the configured vision provider on synthetic frames.
// It never reads real chat contents, writes live memory, or sends WeChat.
func TestLivePagedVideoInspection(t *testing.T) {
	root := os.Getenv("WECHAT_VIDEO_INSPECTION_LIVE_ROOT")
	if root == "" {
		t.Skip("explicit synthetic-video provider validation only")
	}
	c, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal("configuration unavailable")
	}
	f := setup(t)
	c.Behavior.ReadVideos = true
	c.FilehelperEnabled = true
	c.Companion.Permissions.Remember = true
	f.manager.config = c
	p, _, e := f.manager.toolAccess()
	if e != nil {
		t.Fatal(e)
	}
	defer p.hub.Close()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-requester", TraceID: "synthetic-video-inspection"})
	a, e := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: inspectionFixture(t), MIME: "video/mp4", Filename: "synthetic-test-pattern.mp4"}, nil)
	if e != nil {
		t.Fatal("fixture cache failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	start, total, batches := 0, 0, 0
	for {
		raw := json.RawMessage(fmt.Sprintf(`{"action":"frames","asset_id":%q,"start_frame":%d,"page_size":13,"question":"逐帧用极短句描述色块与位置变化"}`, a.ID, start))
		value, e := p.Call(ctx, "video_inspect", raw)
		var page struct {
			Next     int `json:"next_start_frame"`
			Complete bool
			Batches  []struct {
				Indexes  []int `json:"frame_indexes"`
				Analysis string
			}
		}
		if e != nil || json.Unmarshal([]byte(value), &page) != nil {
			t.Fatal("real provider inspection failed")
		}
		for _, b := range page.Batches {
			if b.Analysis == "" {
				t.Fatal("missing vision result")
			}
			for _, i := range b.Indexes {
				if i != total {
					t.Fatal("gap or replay", total, i)
				}
				total++
			}
			batches++
		}
		if page.Complete {
			break
		}
		if page.Next <= start {
			t.Fatal("no continuation progress")
		}
		start = page.Next
	}
	if total != 24 {
		t.Fatal("incomplete video", total)
	}
	t.Logf("actual_decoded_frames=%d provider_batches=%d live_memory_writes=0 wechat_sends=0 synthetic_source_only=true", total, batches)
}
