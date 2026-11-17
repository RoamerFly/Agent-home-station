package admin

import (
	"encoding/json"
	"testing"
	"time"
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
	seed := f.manager.takeMediaSeeds()[0]
	if seed.OriginInput == nil || seed.OriginInput.Mode != "admin_action" || seed.OriginInput.Source != nil || seed.OriginInput.ReplyContext == nil || seed.OriginInput.ReplyContext.State != "system_context" {
		t.Fatal("console request lost its host action identity", seed.OriginInput)
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

func TestConsoleMediaSurvivesUnrelatedMemoryUpdateButNotRevocation(t *testing.T) {
	f, p, _ := forwardFixture(t)
	a, err := f.manager.rememberAssetFormats(companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", TraceID: "file"}, assetOriginal{Data: []byte("synthetic"), MIME: "text/plain", Filename: "fixture.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.login()
	args := map[string]any{"source_thread_id": "source@chatroom", "target_thread_id": p.trace.Thread, "asset_id": a.ID, "kind": "file", "request_key": "memory-independent-file"}
	code, body, _ := f.request("POST", "/api/agent/media-send", args, true, true)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	seeds := f.manager.takeMediaSeeds()
	if len(seeds) != 1 {
		t.Fatal("missing seed")
	}
	if !f.manager.crossSeedAllowed(seeds[0]) {
		t.Fatal("original action rejected")
	}
	if err := f.manager.memory.Observe(companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "synthetic", TraceID: "memory-update", Text: "synthetic context"}, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.memory.SetPreferences(companion.ScopeID("wechat", "test", "source@chatroom"), "synthetic changed preference", 0); err != nil {
		t.Fatal(err)
	}
	if !f.manager.crossSeedAllowed(seeds[0]) {
		t.Fatal("unrelated memory update cancelled exact admin transfer")
	}
	cfg := f.manager.agent.config()
	cfg.CrossConversation = false
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	if f.manager.crossSeedAllowed(seeds[0]) {
		t.Fatal("revoked action continued")
	}
}
