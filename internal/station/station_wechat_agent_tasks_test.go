package station

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/stationservices"
)

func TestStationWechatAgentTaskPersistsAndClaimsOwnerCheckpoint(t *testing.T) {
	dir := t.TempDir()
	owner := func() (stationservices.Owner, error) { return stationservices.Owner{FishID: "one", ID: "owner"}, nil }
	host := &stationWechat{ctx: context.Background(), workers: map[string]*stationWechatWorker{}, ownerReader: owner}
	worker := &stationWechatWorker{connectionID: "one", host: host, dir: dir, runtimeDir: dir, account: "one", policy: stationWechatPolicy{Grants: map[string]time.Time{"room": time.Unix(0, 0).UTC()}}}
	host.workers["one"] = worker
	tools := &stationAttachmentTools{crossHost: host, crossConnection: "one", reads: map[string]time.Time{"room": time.Unix(0, 0).UTC()}, trace: companion.Input{Channel: "wechat", Account: "one", Thread: "room", Sender: "owner", Mode: "direct", Directed: true, TraceID: "incoming", MessageTime: time.Now().UTC().Format(time.RFC3339Nano)}}
	raw := json.RawMessage(`{"key":"repair","title":"检查工具","checkpoint":"继续核对工具状态","state":"waiting","delay_seconds":0}`)
	if _, err := tools.callAgentTaskTool(context.Background(), "agent_task", raw); err != nil {
		t.Fatal(err)
	}
	items, err := worker.readAgentTasks()
	if err != nil || len(items) != 1 || items[0].State != "waiting" {
		t.Fatalf("task was not persisted: %#v, %v", items, err)
	}
	claimed, err := worker.claimAgentTask(time.Now().UTC(), "one", nil)
	if err != nil || claimed == nil || claimed.TaskID != items[0].ID || claimed.Thread != "room" {
		t.Fatalf("task was not claimed: %#v, %v", claimed, err)
	}
	if worker.agentTaskActive(*claimed) == false {
		t.Fatal("claimed task unexpectedly inactive")
	}
	if got := worker.agentTasksFor("one", "other-room", "owner"); len(got) != 0 {
		t.Fatalf("task leaked across conversations: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "queue", stationAgentTaskFileName)); err != nil {
		t.Fatal(err)
	}
}
