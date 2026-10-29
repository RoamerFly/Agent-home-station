package admin

import (
	"encoding/json"
	"testing"
	"wechat-bot/internal/companion"
)

func TestConsoleMediaTransfersUseLiveQueueAndRequireOwnerScope(t *testing.T) {
	f, p, n := forwardFixture(t)
	a, err := f.manager.rememberAssetFormats(companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", TraceID: "file"}, assetOriginal{Data: []byte("synthetic console original"), MIME: "text/plain", Filename: "fixture.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"source_thread_id": "source@chatroom", "target_thread_id": p.trace.Thread, "asset_id": a.ID, "kind": "file", "request_key": "console-file"}
	if code, _, _ := f.request("POST", "/api/agent/media-send", args, true, true); code != 401 {
		t.Fatal("unauthenticated transfer", code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/agent/media-send", args, false, false); code != 403 {
		t.Fatal("CSRF-less transfer", code)
	}
	code, body, _ := f.request("POST", "/api/agent/media-send", args, true, true)
	var out struct {
		ID    string `json:"operation_id"`
		State string
	}
	if code != 200 || json.Unmarshal(body, &out) != nil || out.State != "queued" || len(f.manager.takeMediaSeeds()) != 1 || n.submits.Load() != 0 {
		t.Fatal(code, string(body))
	}
	if code, _, _ := f.request("GET", "/api/agent/media-status/"+out.ID, nil, false, false); code != 200 {
		t.Fatal("status unavailable", code)
	}
	if code, _, _ := f.request("POST", "/api/agent/media-send", args, true, true); code != 200 || len(f.manager.takeMediaSeeds()) != 1 {
		t.Fatal("duplicate console queue", code)
	}
	args["target_thread_id"] = "unknown@chatroom"
	args["request_key"] = "unknown-destination"
	if code, _, _ := f.request("POST", "/api/agent/media-send", args, true, true); code != 400 || len(f.manager.takeMediaSeeds()) != 1 {
		t.Fatal("unknown destination accepted", code)
	}
	c := f.manager.agent.config()
	c.CrossConversation = false
	if err = f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("POST", "/api/agent/media-send", args, true, true); code != 403 {
		t.Fatal("revoked transfer", code)
	}
	if code, _, _ := f.request("GET", "/api/agent/media-status/"+out.ID, nil, false, false); code != 403 {
		t.Fatal("revoked status", code)
	}
}
