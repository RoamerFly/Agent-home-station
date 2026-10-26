package companion

import (
	"strings"
	"testing"
	"time"
)

func TestDeviceObservationIsNotASentReply(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.Permissions.Remember = true
	in := Input{Channel: "wechat", Account: "test", Thread: "room", Sender: "owner", Text: "看看页面", TraceID: "request"}
	now := time.Now()
	if err = store.Observe(in, cfg, now); err != nil {
		t.Fatal(err)
	}
	observation := DeviceObservation{TaskID: "device-synthetic", DeviceID: "lab", State: "completed", Summary: "已看到设置列表"}
	for i := 0; i < 2; i++ {
		if err = store.ObserveDevice(in, cfg, observation, now); err != nil {
			t.Fatal(err)
		}
	}
	scope := store.Context(in, cfg, now)
	if len(scope.Messages) != 2 || scope.Messages[1].Role != "observation" {
		t.Fatal(scope.Messages)
	}
	e := &Engine{Config: cfg}
	turns := e.contextTurns(scope, Input{})
	found := false
	for _, turn := range turns {
		if strings.Contains(turn["content"], "device_tool_observation") {
			found = true
			if turn["role"] != "user" || !strings.Contains(turn["content"], `"sent_to_wechat":false`) {
				t.Fatal(turn)
			}
		}
	}
	if !found {
		t.Fatal("missing structured tool evidence")
	}
}
