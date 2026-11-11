package station

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func exerciseStationWechatCrossGrants(t *testing.T, f *localHomeFixture, saveMessage func(linuxnative.Message), offline func(bool)) {
	t.Helper()
	const origin, source, private = "synthetic-group-a@chatroom", "cross-source@chatroom", "wxid-owner-one"
	id := wechatapp.ConnectionID("one")
	base := "/api/apps/wechat/connections/" + id
	req := func(method, path string, input any, csrf bool, want int) map[string]any {
		t.Helper()
		code, out := f.request(method, path, input, csrf)
		if code != want {
			t.Fatalf("%s %s=%d want %d: %+v", method, path, code, want, out)
		}
		return out
	}
	path := base + "/cross-memory-grants"
	initial := req("GET", path, nil, false, 200)
	if initial["revision"] != float64(0) || len(initial["sources"].(map[string]any)) != 0 || initial["owner_bound"] != true {
		t.Fatal(initial)
	}
	policy := map[string]any{"reply_threads": []string{origin, source, private}, "history_threads": []string{source}}
	req("PUT", base+"/policy", policy, true, 200)
	grant := map[string]any{"revision": 0, "owner_revision": 1, "request_key": "cross-grant", "sources": []string{source}, "origins": []string{origin}}
	req("PUT", path, grant, false, 403)
	bad := map[string]any{"revision": 0, "owner_revision": 1, "request_key": "bad-cross-grant", "sources": []string{source, source}, "origins": []string{origin}}
	req("PUT", path, bad, true, 400)
	bad["sources"] = []string{origin}
	req("PUT", path, bad, true, 409)
	bad["sources"] = []string{source}
	bad["origins"] = []string{"ungranted@chatroom"}
	req("PUT", path, bad, true, 409)
	bad["origins"] = []string{origin}
	bad["owner_revision"] = 0
	req("PUT", path, bad, true, 409)
	allowed := req("PUT", path, grant, true, 200)
	req("PUT", path, grant, true, 200)
	if allowed["revision"] != float64(1) || allowed["private_reply_authorized"] != true || allowed["scopes_current"] != true {
		t.Fatal(allowed)
	}
	grant["revision"] = 1
	req("PUT", path, grant, true, 409)
	grant["revision"] = 0
	station := req("GET", base+"/memory-grants", nil, false, 200)
	if station["revision"] != float64(0) || len(station["station_reads"].(map[string]any)) != 0 {
		t.Fatal("reverse authorization opened Station direction", station)
	}
	other := req("GET", "/api/apps/wechat/connections/"+wechatapp.ConnectionID("two")+"/cross-memory-grants", nil, false, 200)
	if other["revision"] != float64(0) || other["owner_bound"] != false {
		t.Fatal("cross-account authority", other)
	}
	owner, err := f.handler.home.applicationOwner()
	if err != nil {
		t.Fatal(err)
	}
	text := "查一下获准来源中的合成旅行资料"
	message := linuxnative.Message{ID: "cross-owner", AccountID: "one", ThreadID: origin, SenderID: private, Body: text, Direction: "incoming", Type: 1, ServerID: "98765", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	saveMessage(message)
	in := companion.Input{Channel: "wechat", Account: "one", Thread: origin, Sender: private, TraceID: message.ID, Text: text, OriginalText: &text, MessageTime: message.CreatedAt, IsGroup: true, Mode: "direct", Directed: true}
	in.Source = &companion.SourceRef{Account: in.Account, Thread: in.Thread, Sender: in.Sender, MessageID: in.TraceID, ServerID: message.ServerID, OccurredAt: in.MessageTime}
	host := f.handler.chat.wechat
	proof, err := host.verifyWechatCrossRequest(t.Context(), id, in, source, owner)
	if err != nil || proof.Digest == "" || proof.PrivateTarget != private || proof.GrantRevision != 1 {
		t.Fatal(proof, err)
	}
	deny := func(input companion.Input, source string) {
		t.Helper()
		if got, err := host.verifyWechatCrossRequest(t.Context(), id, input, source, owner); err == nil || got.Digest != "" {
			t.Fatal("unauthorized request", got)
		}
	}
	for _, change := range []func(*companion.Input){
		func(i *companion.Input) { i.Sender = "wxid-member" }, func(i *companion.Input) { i.Account = "two" },
		func(i *companion.Input) { i.Thread = "other@chatroom" }, func(i *companion.Input) { i.IsGroup = false },
		func(i *companion.Input) { i.Directed = false }, func(i *companion.Input) { i.Mode = "social" },
		func(i *companion.Input) { i.Preview = true }, func(i *companion.Input) { i.Surf = true }, func(i *companion.Input) { i.HistoricalReview = true },
		func(i *companion.Input) { i.ReportOnly = true }, func(i *companion.Input) { i.ConversationDelivery = true }, func(i *companion.Input) { i.OriginTraceID = "other" },
		func(i *companion.Input) { i.OriginalText = nil }, func(i *companion.Input) { v := "篡改原话"; i.OriginalText = &v },
		func(i *companion.Input) { i.MessageTime = time.Now().Add(-time.Hour).Format(time.RFC3339Nano) },
		func(i *companion.Input) { ref := *i.Source; ref.ServerID = "wrong"; i.Source = &ref },
	} {
		copy := in
		change(&copy)
		deny(copy, source)
	}
	deny(in, origin)
	deny(in, "ungranted@chatroom")
	foreign := owner
	foreign.ID = "other-owner"
	if _, err := host.verifyWechatCrossRequest(t.Context(), id, in, source, foreign); err == nil {
		t.Fatal("foreign local owner accepted")
	}
	message.SenderID = "wxid-member"
	saveMessage(message)
	deny(in, source)
	message.SenderID = private
	message.Type = 49
	saveMessage(message)
	deny(in, source)
	message.Type = 1
	message.Direction = "outgoing"
	saveMessage(message)
	deny(in, source)
	message.Direction = "incoming"
	message.Body = "改过的原话"
	saveMessage(message)
	deny(in, source)
	message.Body = text
	saveMessage(message)
	worker, _ := host.worker(id, false)
	// Native reads and authorization never open a cross-source database.
	if _, err := os.Stat(filepath.Join(worker.dir, "memory")); !os.IsNotExist(err) {
		t.Fatal("authorization preloaded memory", err)
	}
	policy["reply_threads"] = []string{origin, source}
	req("PUT", base+"/policy", policy, true, 200)
	deny(in, source)
	policy["reply_threads"] = []string{origin, source, private}
	req("PUT", base+"/policy", policy, true, 200)
	message.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	saveMessage(message)
	in.MessageTime = message.CreatedAt
	in.Source.OccurredAt = in.MessageTime
	newProof, err := host.verifyWechatCrossRequest(t.Context(), id, in, source, owner)
	if err != nil || newProof.Digest == proof.Digest {
		t.Fatal("private grant replacement not bound", newProof, err)
	}
	// Removing and restoring a history grant cannot revive saved source access.
	policy["history_threads"] = []string{}
	req("PUT", base+"/policy", policy, true, 200)
	deny(in, source)
	policy["history_threads"] = []string{source}
	req("PUT", base+"/policy", policy, true, 200)
	deny(in, source)
	if stale := req("GET", path, nil, false, 200); stale["scopes_current"] != false {
		t.Fatal("stale history scope reported current", stale)
	}
	grant["revision"], grant["request_key"] = 1, "cross-regrant"
	req("PUT", path, grant, true, 200)
	message.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	saveMessage(message)
	in.MessageTime = message.CreatedAt
	in.Source.OccurredAt = in.MessageTime
	if _, err := host.verifyWechatCrossRequest(t.Context(), id, in, source, owner); err != nil {
		t.Fatal(err)
	}
	// Rebinding the same person still changes authority; old grants stay invalid.
	worker.ownerMu.Lock()
	binding, err := readStationWechatOwner(worker, "one")
	if err != nil {
		t.Fatal(err)
	}
	binding.Revision++
	if err := worker.saveWechatOwner(binding); err != nil {
		t.Fatal(err)
	}
	worker.ownerMu.Unlock()
	deny(in, source)
	offline(true)
	got := req("GET", path, nil, false, 200)
	if got["revision"] != float64(2) {
		t.Fatal(got)
	}
	req("PUT", path, map[string]any{"revision": 2, "owner_revision": 1, "request_key": "offline-revoke-cross", "sources": []string{}, "origins": []string{}}, true, 200)
	offline(false)
	deny(in, source)
	saved := mustReadSnapshotFixture(t, filepath.Join(worker.dir, stationWechatCrossGrantsFile))
	if strings.Contains(string(saved), text) || strings.Contains(string(saved), message.ServerID) {
		t.Fatal("authorization retained original text")
	}
	var restored stationWechatCrossGrants
	if json.Unmarshal(saved, &restored) != nil || restored.Revision != 3 || len(restored.Sources) != 0 || len(restored.Origins) != 0 {
		t.Fatal(restored)
	}
	// Restore only the binding fixture used by the surrounding restart tests.
	worker.ownerMu.Lock()
	binding.Revision--
	if err := worker.saveWechatOwner(binding); err != nil {
		t.Fatal(err)
	}
	worker.ownerMu.Unlock()
	req("PUT", base+"/policy", map[string]any{"reply_threads": []string{}, "history_threads": []string{}}, true, 200)
}
