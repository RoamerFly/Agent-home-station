package admin

import (
	"context"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
)

func TestExplicitSourceActorKeepsActorAndOriginSeparateAndRechecks(t *testing.T) {
	f, native, _, _ := receivedPolicyFixture(t, false)
	tools := &permittedTools{manager: f.manager, trace: companion.Input{Channel: "wechat", Account: "test", Thread: native.msg.ThreadID, Sender: "current-actor", TraceID: "actor-turn", Mode: "direct"}}
	ctx := withSourceActor(context.Background(), tools, "opaque-request-key", native.msg.ThreadID)
	policy, err := f.manager.newReceivedSharePolicy(ctx, native.msg)
	if err != nil {
		t.Fatal(err)
	}
	invocation := policy.invocation()
	if invocation.Requester != "current-actor" || invocation.OriginSourceSender != native.msg.SenderID || invocation.RequestThread != native.msg.ThreadID || invocation.RequestKey == "" {
		t.Fatalf("actor provenance was not retained: %#v", invocation)
	}
	policy.tool = "media.observe"
	mediaInvocation := policy.invocation()
	if mediaInvocation.Requester != "current-actor" || mediaInvocation.RequestKey == "" || mediaInvocation.RequestKey == "received-media-"+agentID(policy.source.EventID, policy.message.ID, policy.fingerprint, policy.requestSuffix) {
		t.Fatalf("media invocation dropped actor request identity: %#v", mediaInvocation)
	}
	policy.tool = "share.resolve_channels"
	job := pluginhost.Job{Plugin: "share-reader", Tool: "share.resolve_channels", Context: invocation}
	if err = policy.Check(ctx, job, pluginhost.Capability{}); err != nil {
		t.Fatalf("current actor unexpectedly rejected: %v", err)
	}
	tools.trace.TaskID = "different-task"
	if err = policy.Check(ctx, job, pluginhost.Capability{}); err == nil || err.Error() != "source_actor_authorization_changed" {
		t.Fatalf("task identity switch accepted: %v", err)
	}
	tools.trace.TaskID = ""
	// Simulate revocation/turn replacement after source selection. A policy may
	// retain the original uploader as evidence, but cannot continue under a
	// changed current actor context.
	tools.trace.Sender = "revoked-actor"
	if err = policy.Check(ctx, job, pluginhost.Capability{}); err == nil || err.Error() != "source_actor_authorization_changed" {
		t.Fatalf("revoked actor accepted: %v", err)
	}
	tools.trace.Sender = "current-actor"
	settings := f.manager.agent.config()
	settings.Enabled = false
	if err = f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	if err = policy.Check(ctx, job, pluginhost.Capability{}); err == nil || err.Error() != "source_actor_authorization_changed" {
		t.Fatalf("disabled agent retained source actor authority: %v", err)
	}
	if _, err = f.manager.newReceivedSharePolicy(withSourceActor(context.Background(), tools, "", native.msg.ThreadID), native.msg); err == nil || err.Error() != "source_actor_not_authorized" {
		t.Fatalf("invalid explicit actor proof became automatic: %v", err)
	}
}

func TestExplicitSourceActorSupportsAuthorizedSurfFactoryShape(t *testing.T) {
	f, native, _, _ := receivedPolicyFixture(t, false)
	f.manager.mu.Lock()
	f.manager.config.DirectIDs = append(f.manager.config.DirectIDs, native.msg.ThreadID)
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Proactive = true
	f.manager.config.Companion.Permissions.ReplyText = true
	f.manager.config.Companion.QuietStart, f.manager.config.Companion.QuietEnd = "00:00", "00:00"
	f.manager.surf.Settings.Enabled = true
	f.manager.surf.Settings.Threads = []string{native.msg.ThreadID}
	f.manager.mu.Unlock()
	// claimSurf creates a surf turn without a TaskID and with no raw sender.
	// The canonical requester is host:surf; no persisted task owner is used.
	tools := &permittedTools{manager: f.manager, trace: companion.Input{Channel: "wechat", Account: "test", Thread: native.msg.ThreadID, Sender: "", TraceID: "surf-factory-shape", Surf: true, Mode: "proactive"}}
	ctx := withSourceActor(context.Background(), tools, "surf-request", native.msg.ThreadID)
	policy, err := f.manager.newReceivedSharePolicy(ctx, native.msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.invocation(); got.Requester != "host:surf" || got.OriginSourceSender != native.msg.SenderID || got.RequestThread != native.msg.ThreadID {
		t.Fatalf("surf actor provenance invalid: %#v", got)
	}
	if !policy.actor.active() {
		t.Fatal("authorised surf proof rejected")
	}
}

func TestExplicitSourceActorRejectsCancelledTask(t *testing.T) {
	f, native, _, _ := receivedPolicyFixture(t, false)
	const taskID = "actor-task"
	if err := f.manager.agent.task(agentTask{ID: taskID, Account: "test", Thread: native.msg.ThreadID, Sender: "current-actor", State: "running"}); err != nil {
		t.Fatal(err)
	}
	tools := &permittedTools{manager: f.manager, trace: companion.Input{Channel: "wechat", Account: "test", Thread: native.msg.ThreadID, Sender: "current-actor", TraceID: "actor-task-turn", TaskID: taskID, Mode: "direct"}}
	ctx := withSourceActor(context.Background(), tools, "opaque-task-request", native.msg.ThreadID)
	policy, err := f.manager.newReceivedSharePolicy(ctx, native.msg)
	if err != nil {
		t.Fatal(err)
	}
	job := pluginhost.Job{Plugin: "share-reader", Tool: "share.resolve_channels", Context: policy.invocation()}
	if err = policy.Check(ctx, job, pluginhost.Capability{}); err != nil {
		t.Fatal(err)
	}
	if err = f.manager.agent.task(agentTask{ID: taskID, Account: "test", Thread: native.msg.ThreadID, Sender: "current-actor", State: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if err = policy.Check(ctx, job, pluginhost.Capability{}); err == nil || err.Error() != "source_actor_authorization_changed" {
		t.Fatalf("cancelled task retained provider authority: %v", err)
	}
}

func TestExplicitSourceActorRechecksCrossConversationPolicyWithoutTraceChange(t *testing.T) {
	f, _, _, _ := receivedPolicyFixture(t, false)
	const actor, sourceThread = "current-actor", "other-conversation"
	if err := f.manager.rememberNativeConversations(map[string]string{sourceThread: "synthetic other"}); err != nil {
		t.Fatal(err)
	}
	settings := f.manager.agent.config()
	settings.Enabled, settings.CrossConversation = true, true
	settings.OwnerIDs, settings.PrivilegedThreads = []string{actor}, []string{"*"}
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	tools := &permittedTools{manager: f.manager, trace: companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: actor, TraceID: "cross-turn", Mode: "direct"}}
	ctx := withSourceActor(context.Background(), tools, "cross-request", sourceThread)
	proof, present := sourceActorFromContext(ctx)
	if !present || !proof.active() {
		t.Fatal("authorised cross-conversation proof was not active")
	}
	settings = f.manager.agent.config()
	settings.CrossConversation = false
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	if proof.active() {
		t.Fatal("cross-conversation revocation did not invalidate proof")
	}
}
