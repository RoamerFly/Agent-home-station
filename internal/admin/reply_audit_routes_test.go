package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestReplyAuditRoutesAreAccountScopedAndReadOnly(t *testing.T) {
	f := setup(t)
	f.login()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "member-a", TraceID: "synthetic-incoming", Text: "晚安"}
	c := f.manager.Config().Companion
	c.Permissions.Remember = true
	if err := f.manager.memory.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	scope := companion.ScopeID(in.Channel, in.Account, in.Thread)
	id := strings.Repeat("a", 64)
	audit := companion.ReplyAudit{ID: id, Scope: scope, At: time.Now().UTC(), TriggerMessageID: in.TraceID, Sender: in.Sender, State: "validated", Contexts: []companion.ReplyContext{{TargetIDs: []string{in.Sender}, AnchorMessageID: in.TraceID}}}
	dir := filepath.Join(f.manager.dir, "companion", "reply-audits", scope)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(audit)
	if err := os.WriteFile(filepath.Join(dir, id+".meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	code, body, _ := f.request("GET", "/api/reply-audits?thread_id=room@chatroom&message_id=synthetic-incoming", nil, true, true)
	if code != http.StatusOK || !strings.Contains(string(body), id) {
		t.Fatal(code, string(body))
	}
	for _, path := range []string{"/api/reply-audits?thread_id=unknown@chatroom", "/api/memory/" + companion.ScopeID("wechat", "another-account", in.Thread) + "/reply-audits"} {
		code, _, _ = f.request("GET", path, nil, true, true)
		if code != http.StatusNotFound {
			t.Fatal("cross-account audit exposed", path, code)
		}
	}
	code, body, _ = f.request("GET", "/api/agent/runtime-capabilities", nil, true, true)
	if code != 200 || !strings.Contains(string(body), `"read_only":true`) {
		t.Fatal(code, string(body))
	}
}

func TestSourceConflictManagementRejectsCrossScopeAndMissingCSRF(t *testing.T) {
	f := setup(t)
	f.login()
	c := f.manager.Config().Companion
	c.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "member", TraceID: "source", Text: "原文"}
	if err := f.manager.memory.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	scope := companion.ScopeID(in.Channel, in.Account, in.Thread)
	path := "/api/memory/" + scope + "/source-conflicts"
	code, _, _ := f.request("POST", path, map[string]any{"items": []companion.SourceAudit{{Scope: "other"}}}, true, false)
	if code != 403 {
		t.Fatal("CSRF bypass", code)
	}
	f.manager.setState("disabled")
	code, _, _ = f.request("POST", path, map[string]any{"items": []companion.SourceAudit{{Scope: "other"}}}, true, true)
	if code != 403 {
		t.Fatal("cross-scope quarantine admitted", code)
	}
}
