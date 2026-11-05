package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type crossRouteNative struct {
	*fakeNative
	requests []linuxnative.ToolSend
	state    string
	last     linuxnative.ToolStatus
}

func (*crossRouteNative) Capabilities(context.Context) (linuxnative.Capabilities, error) {
	return linuxnative.Capabilities{}, nil
}
func (*crossRouteNative) Upload(context.Context, string, string, io.Reader, int64) (linuxnative.UploadSource, error) {
	return linuxnative.UploadSource{}, errors.New("not used")
}
func (n *crossRouteNative) Submit(_ context.Context, in linuxnative.ToolSend) (linuxnative.ToolStatus, error) {
	n.requests = append(n.requests, in)
	state := n.state
	if state == "" {
		state = "accepted"
	}
	serverID := "server-private"
	if state != "accepted" {
		serverID = ""
	}
	n.last = linuxnative.ToolStatus{ID: "cross-private", ThreadID: in.ThreadID, State: state, ServerID: serverID, Kind: in.Kind}
	return n.last, nil
}
func (n *crossRouteNative) LookupSend(context.Context, string) (linuxnative.ToolStatus, error) {
	if n.last.ID != "" {
		return n.last, nil
	}
	return linuxnative.ToolStatus{}, linuxnative.ErrToolNotFound
}
func (*crossRouteNative) ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error) {
	return linuxnative.ToolStatus{}, linuxnative.ErrToolNotFound
}

func TestCrossConversationGroupResultRoutesOnlyToRequestingOwner(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.CrossConversation = true
	cfg.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "source@chatroom", Mode: "mention"})
	text := "请读取另一会话并总结"
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "wxid_owner", TraceID: "group-owner-request", Text: text, OriginalText: &text, IsGroup: true, Mode: "admin_action", MessageTime: time.Now().UTC().Format(time.RFC3339Nano)}
	p.SetTrace(in)
	native := &crossRouteNative{fakeNative: f.native}
	f.manager.native = native
	p.routeCrossConversationReply()
	if got := p.PrivateReplyTarget(); got != "wxid_owner" {
		t.Fatalf("private target = %q", got)
	}
	if err := p.RoutePrivateReply(t.Context(), []string{"这是按来源读取后的结果。"}); err != nil {
		t.Fatal(err)
	}
	if len(native.requests) != 1 || native.requests[0].ThreadID != "wxid_owner" || native.requests[0].Kind != "text" {
		t.Fatalf("wrong destination: %#v", native.requests)
	}
	if native.requests[0].Quote != nil || strings.Contains(native.requests[0].Text, "source@chatroom") {
		t.Fatalf("source group leaked into private submit: %#v", native.requests[0])
	}
}

func TestGlobalSearchRoutesOnlyWhenReturnedSourceIsAnotherConversation(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.CrossConversation = true
	cfg.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "room@chatroom", Mode: "mention"})
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "wxid_owner", IsGroup: true, Mode: "direct"})
	p.routeGlobalSearchResult(`{"items":[{"thread_id":"room@chatroom"}]}`)
	if p.PrivateReplyTarget() != "" {
		t.Fatal("current conversation search unexpectedly rerouted")
	}
	p.routeGlobalSearchResult(`{"items":[{"thread_id":"other@chatroom"}]}`)
	if p.PrivateReplyTarget() != "wxid_owner" {
		t.Fatal("cross conversation result did not route to owner")
	}
}

func TestAgentContextIsLazyIndexNotWorkingNotes(t *testing.T) {
	f, p := trustedAgent(t)
	guide, err := f.manager.workspace.Read("shared", "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.workspace.Write("shared", "AGENTS.md", "private-long-guide-token", guide.Revision); err != nil {
		t.Fatal(err)
	}
	context := p.AgentContext(t.Context())
	for _, forbidden := range []string{"private-long-guide-token", "execution_context", "registered_projects"} {
		if strings.Contains(context, forbidden) {
			t.Fatalf("first turn eagerly loaded %q: %s", forbidden, context)
		}
	}
	if !strings.Contains(context, "agent_workspace_index") || !strings.Contains(context, "workspace_read/search") {
		t.Fatalf("lazy workspace discovery missing: %s", context)
	}
}

func TestCrossConversationTaskPersistsSourceAndPrivateDelivery(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.CrossConversation = true
	cfg.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "request@chatroom", Mode: "mention"}, GroupRule{ID: "source@chatroom", Mode: "mention"})
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "request@chatroom", Sender: "wxid_owner", TraceID: "cross-task", IsGroup: true, Mode: "direct"})
	p.routeCrossConversationThread("source@chatroom") // successful read evidence
	p.recordCrossSourceMessages("source@chatroom", `{"items":[{"id":"oldest"},{"id":"second"},{"id":"third"}]}`)
	result, err := p.Call(t.Context(), "agent_task", []byte(`{"key":"followup","title":"展开第二条","checkpoint":"读取下一条来源","state":"waiting","source_thread_id":"source@chatroom","source_message_ids":["oldest","second","third"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Task agentTask `json:"task"`
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatal(err)
	}
	if out.Task.SourceThread != "source@chatroom" || out.Task.RequestThread != "request@chatroom" || out.Task.DeliveryThread != "wxid_owner" || out.Task.Thread != "wxid_owner" {
		t.Fatalf("unexpected scope: %#v", out.Task)
	}
	if out.Task.Constraints == nil || out.Task.Constraints.Thread != "source@chatroom" || out.Task.Constraints.DeliveryThread != "wxid_owner" {
		t.Fatalf("constraints lost source/delivery: %#v", out.Task.Constraints)
	}
	if got := strings.Join(out.Task.SourceMessageIDs, ","); got != "oldest,second,third" {
		t.Fatalf("source order was not persisted: %q", got)
	}
	// A later “第二条再展开” continuation receives only the ordered, verified
	// source IDs. It must reread that exact source rather than inherit a whole
	// cross-group transcript as owner-private chat context.
	f.manager.config.Companion.Enabled = true
	claimed, err := f.manager.claimAgentTask(time.Now().Add(time.Second), func(string) bool { return false })
	if err != nil || claimed == nil {
		t.Fatalf("cross task was not resumable: input=%#v err=%v", claimed, err)
	}
	if !strings.Contains(claimed.Text, `"source_message_ids":["oldest","second","third"]`) || strings.Contains(claimed.Text, "跨群完整正文") {
		t.Fatalf("continuation did not preserve a narrow ordered source reference: %s", claimed.Text)
	}
}

func TestOwnerCanCancelSpecifiedCrossConversationTask(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.CrossConversation = true
	cfg.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "source@chatroom", Mode: "mention"})
	task := agentTask{ID: "cross-cancel-task", Account: "test", Thread: "wxid_owner", RequestThread: "wxid_owner", SourceThread: "source@chatroom", DeliveryThread: "wxid_owner", Sender: "wxid_owner", State: "waiting", Next: time.Now().Add(time.Hour)}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "stop-cross-task", Mode: "direct"})
	if _, err := p.Call(t.Context(), "agent_task", []byte(`{"target_task_id":"cross-cancel-task","checkpoint":"发错了，停止剩余部分","state":"cancelled"}`)); err != nil {
		t.Fatal(err)
	}
	stored, ok := f.manager.agent.lookupTask(task.ID, task.Account, "")
	if !ok || stored.State != "cancelled" {
		t.Fatalf("task not cancelled: %#v", stored)
	}
	if f.manager.agentTaskActive(companion.Input{Account: "test", Thread: "wxid_owner", TaskID: task.ID}) {
		t.Fatal("cancelled task remained active")
	}
}

func TestCrossConversationReadRequiresPrivateRouteEvenWhenOwnerPrivateDisabled(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.CrossConversation = true
	cfg.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "request@chatroom", Mode: "mention"})
	// The source group remains enabled, but the owner private chat is removed.
	f.manager.config.DirectIDs = nil
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "request@chatroom", Sender: "wxid_owner", TraceID: "cross-no-private", IsGroup: true, Mode: "direct"})
	p.routeCrossConversationThread("source@chatroom")
	if !p.PrivateReplyRequired() || p.PrivateReplyTarget() != "" {
		t.Fatalf("cross source must be private-only even when target unavailable: required=%v target=%q", p.PrivateReplyRequired(), p.PrivateReplyTarget())
	}
}

func TestRecoveryPrivacyBindingReauthorizesRouteAfterSetTrace(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.CrossConversation = true
	cfg.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.GroupRules = append(f.manager.config.GroupRules,
		GroupRule{ID: "request@chatroom", Mode: "mention"},
		GroupRule{ID: "source@chatroom", Mode: "mention"},
	)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "request@chatroom", Sender: "wxid_owner", TraceID: "cross-recovery", IsGroup: true, Mode: "direct"}
	p.SetTrace(in)
	p.routeCrossConversationThread("source@chatroom")
	binding := p.RecoveryPrivacyBinding()
	if binding == nil || !binding.Required || binding.Target != "wxid_owner" || strings.Join(binding.SourceThreads, ",") != "source@chatroom" {
		t.Fatal("cross source binding missing", binding)
	}
	// This emulates a fresh engine run: SetTrace clears per-run state and then
	// restores it only after checking current owner, account, sources and route.
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "request@chatroom", Sender: "wxid_owner", TraceID: "cross-retry", IsGroup: true, Mode: "direct", RecoveryPrivacy: binding})
	if err := p.RecoveryRouteValid(); err != nil || !p.PrivateReplyRequired() || p.PrivateReplyTarget() != "wxid_owner" {
		t.Fatal("authorized recovery route was not rebuilt", err, p.PrivateReplyRequired(), p.PrivateReplyTarget())
	}
	// A revoked private target must block before any saved result is reused.
	f.manager.config.DirectIDs = nil
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "request@chatroom", Sender: "wxid_owner", TraceID: "cross-retry-revoked", IsGroup: true, Mode: "direct", RecoveryPrivacy: binding})
	if p.RecoveryRouteValid() == nil || p.PrivateReplyRequired() {
		t.Fatal("revoked recovery route remained usable")
	}
}

func TestCrossConversationPrivateRouteKeepsOneQueuedOperation(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.CrossConversation = true
	cfg.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "source@chatroom", Mode: "mention"})
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "wxid_owner", TraceID: "queued-private", IsGroup: true, Mode: "admin_action"})
	native := &crossRouteNative{fakeNative: f.native, state: "queued"}
	f.manager.native = native
	p.routeCrossConversationThread("other@chatroom")
	if err := p.RoutePrivateReply(t.Context(), []string{"已排队的私聊结果"}); err != nil {
		t.Fatal(err)
	}
	// A model retry before a late native receipt must reuse the operation and
	// native idempotency key rather than submit another private message.
	if err := p.RoutePrivateReply(t.Context(), []string{"改写后的结果不能重发"}); err != nil {
		t.Fatal(err)
	}
	if len(native.requests) != 1 || native.requests[0].ThreadID != "wxid_owner" {
		t.Fatalf("queued route was replayed or leaked: %#v", native.requests)
	}
}

func TestCrossConversationPrivateRouteDoesNotTreatFailedReceiptAsSent(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.CrossConversation = true
	cfg.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "source@chatroom", Mode: "mention"})
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "wxid_owner", TraceID: "failed-private", IsGroup: true, Mode: "admin_action"})
	native := &crossRouteNative{fakeNative: f.native, state: "failed"}
	f.manager.native = native
	p.routeCrossConversationThread("other@chatroom")
	if err := p.RoutePrivateReply(t.Context(), []string{"不应宣称已送达"}); err == nil {
		t.Fatal("failed receipt was treated as a delivered private reply")
	}
	if len(native.requests) != 1 {
		t.Fatalf("unexpected native submissions: %#v", native.requests)
	}
}
