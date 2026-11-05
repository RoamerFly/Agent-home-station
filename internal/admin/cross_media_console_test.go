package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/linuxnative"
)

func TestConsoleCrossMediaRequiresAuthenticatedExplicitAuthorizedRoute(t *testing.T) {
	f, _, n := forwardFixture(t)
	input := map[string]string{"owner_id": "wxid_owner", "source_thread_id": "source@chatroom", "target_thread_id": "wxid_owner", "message_id": "file-message", "kind": "file", "request_key": "console-file-one"}
	route := "/api/agent/media-batches"
	if code, _, _ := f.request("POST", route, input, true, true); code != 401 {
		t.Fatal("unauthenticated", code)
	}
	f.login()
	if code, _, _ := f.request("POST", route, input, true, false); code != 403 {
		t.Fatal("csrf", code)
	}
	for _, change := range []map[string]string{{"owner_id": "imposter"}, {"target_thread_id": ""}, {"source_thread_id": ""}, {"target_thread_id": "other-account@chatroom"}} {
		bad := map[string]string{}
		for k, v := range input {
			bad[k] = v
		}
		for k, v := range change {
			bad[k] = v
		}
		if code, _, _ := f.request("POST", route, bad, true, true); code < 400 {
			t.Fatal("invalid route accepted", change, code)
		}
	}
	if n.downloads != 0 || n.submits.Load() != 0 {
		t.Fatal("denied actions had effects")
	}
	code, data, _ := f.request("POST", route, input, true, true)
	var response struct {
		Result struct {
			ID     string `json:"operation_id"`
			Target string `json:"target_thread_id"`
		} `json:"result"`
		Replies int `json:"conversational_replies"`
	}
	if code != 202 || json.Unmarshal(data, &response) != nil || response.Result.ID == "" || response.Result.Target != input["target_thread_id"] || response.Replies != 0 {
		t.Fatal(code, string(data))
	}
	seeds := f.manager.takeMediaSeeds()
	if len(seeds) != 1 || seeds[0].Input.Thread != input["target_thread_id"] || n.submits.Load() != 0 {
		t.Fatal("wrong route or immediate submission", seeds)
	}
	origin := seeds[0].OriginInput
	if origin == nil || origin.Mode != "admin_action" || origin.Source != nil || origin.OriginTraceID == "" || origin.ReplyContext == nil || origin.ReplyContext.State != "system_context" || origin.ReplyContext.EvidenceEpoch == "" || origin.ReplyContext.EvidenceAuthorization == "" {
		t.Fatal("authenticated action was forged as a chat or lacked durable proof", origin)
	}
	if code, _, _ = f.request("POST", route, input, true, true); code != 202 || n.downloads != 1 || n.submits.Load() != 0 {
		t.Fatal("duplicate invocation replayed", code, n.downloads)
	}
	input["target_thread_id"] = "source@chatroom"
	if code, _, _ = f.request("POST", route, input, true, true); code != 400 {
		t.Fatal("same key changed destination", code)
	}
	batch, err := f.manager.readCrossBatch(response.Result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.manager.settleCrossMediaPart(batch.ID, batch.Parts[0].Key, acceptedCrossPart(batch, 0)); err != nil {
		t.Fatal(err)
	}
	// A genuinely new console request may send a completed asset again. It
	// must not inherit a constant console-wide identity and reuse it forever.
	input["target_thread_id"] = "wxid_owner"
	input["request_key"] = "console-file-two"
	code, data, _ = f.request("POST", route, input, true, true)
	oldID := response.Result.ID
	if code != 202 || json.Unmarshal(data, &response) != nil || response.Result.ID == oldID || n.submits.Load() != 0 {
		t.Fatal("new explicit request reused completed batch", code, string(data))
	}
}

func TestConsoleCrossReconcilePersistsReceiptsWithoutSubmission(t *testing.T) {
	f, _, b := crossRecoveryFixture(t, 1)
	n := &receiptLookupNative{forwardNative: f.manager.native.(*forwardNative), statuses: map[string]linuxnative.ToolStatus{b.Parts[0].Key: acceptedCrossPart(b, 0)}}
	f.manager.native = n
	route := "/api/agent/media-batches/" + b.ID + "/reconcile"
	if code, _, _ := f.request("POST", route, nil, true, true); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("POST", route, nil, true, false); code != 403 {
		t.Fatal(code)
	}
	code, data, _ := f.request("POST", route, nil, true, true)
	if code != 200 || !strings.Contains(string(data), `"state":"accepted"`) || n.submits.Load() != 0 {
		t.Fatal(code, string(data))
	}
	saved, err := f.manager.readCrossBatch(b.ID)
	if err != nil || !crossBatchAccepted(saved) {
		t.Fatal("receipt not persisted", err)
	}
	c := f.manager.agent.config()
	c.CrossConversation = false
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("POST", route, nil, true, true); code != 403 {
		t.Fatal("revocation ignored", code)
	}
}
