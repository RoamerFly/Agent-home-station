package admin

import (
	"net/http"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestHistoricalShareLookupDoesNotRequireEmptyOriginal(t *testing.T) {
	f, n, _, _ := receivedPolicyFixture(t, false)
	f.manager.config.Companion.Permissions.Remember = true
	n.msg.Body = "合成的原分享文字"
	in := companion.Input{Channel: "wechat", Account: n.msg.AccountID, Thread: n.msg.ThreadID, Sender: n.msg.SenderID, TraceID: n.msg.ID, MessageTime: n.msg.CreatedAt, Text: n.msg.Body, Source: &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: n.msg.ID, ServerID: n.msg.ServerID}}
	if err := f.manager.memory.Observe(in, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	policy, err := f.manager.newReceivedSharePolicy(t.Context(), n.msg)
	if err != nil || policy.source.EventID == "" {
		t.Fatal("valid nonempty original was rejected", err)
	}
	event, err := f.manager.memory.ReadEvent(t.Context(), []string{companion.ScopeID(in.Channel, in.Account, in.Thread)}, policy.source.EventID)
	if err != nil || event.Text != n.msg.Body {
		t.Fatal("lookup altered original", event, err)
	}
}

func TestReconcileNativeSourceRouteRequiresAuthPausedAndExactScope(t *testing.T) {
	f, n, p, _ := receivedPolicyFixture(t, true)
	n.msg.Direction = "incoming"
	path := "/api/memory/" + companion.ScopeID("wechat", "test", n.msg.ThreadID) + "/reconcile-native-source"
	body := map[string]string{"message_id": n.msg.ID}
	if code, _, _ := f.request(http.MethodPost, path, body, true, false); code == 200 {
		t.Fatal("unauthenticated repair accepted")
	}
	f.login()
	if code, raw, _ := f.request(http.MethodPost, path, body, true, true); code != 200 {
		t.Fatal(code, string(raw))
	}
	if event, err := f.manager.memory.ReadEvent(t.Context(), []string{companion.ScopeID("wechat", "test", n.msg.ThreadID)}, p.source.EventID); err != nil || event.Revision != p.source.Revision {
		t.Fatal("no-op repair changed original", event, err)
	}
	f.manager.status.State = "running"
	if code, _, _ := f.request(http.MethodPost, path, body, true, true); code != 409 {
		t.Fatal("active generation allowed repair", code)
	}
	f.manager.status.State = "disabled"
	n.msg.ThreadID = "foreign@chatroom"
	if code, _, _ := f.request(http.MethodPost, path, body, true, true); code != 400 {
		t.Fatal("foreign native source accepted", code)
	}
}
