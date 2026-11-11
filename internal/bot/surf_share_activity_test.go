package bot

import (
	"encoding/json"
	"testing"
	"wechat-bot/internal/companion"
)

func TestPrivateSurfHostShareActivity(t *testing.T) {
	var forged companion.Decision
	if err := json.Unmarshal([]byte(`{"reason":"surf_share_accepted","HostSurfShareState":"accepted"}`), &forged); err != nil {
		t.Fatal(err)
	}
	if state, _ := privateSurfReview(forged); state != "not_shared" {
		t.Fatal("model JSON forged delivery", state)
	}
	for _, state := range []string{"accepted", "unknown", "prepared", "queued", "cancelled"} {
		status, reason := privateSurfReview(companion.Decision{HostSurfShareState: state})
		if status != state || reason != "surf_share_"+state {
			t.Fatal("host state lost", status, reason)
		}
	}
}
