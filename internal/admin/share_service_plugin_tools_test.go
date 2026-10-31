package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
)

func TestShareServiceArgumentsExposeOnlyNativeSelectorAndPaging(t *testing.T) {
	in, err := decodeShareServiceArguments(json.RawMessage(`{"source_message_id":"share-one","offset":3,"limit":7,"candidate_offset":2,"candidate_limit":4,"timeout_seconds":45}`))
	if err != nil || in.timeoutFor("share.resolve_public") != 45 || in.timeoutFor("share.resolve_channels") != 45 {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"source_message_id":"share-one","url":"https://private.example/"}`,
		`{"source_message_id":"share-one","export_id":"opaque"}`,
		`{"source_message_id":"share-one","media_url":"https://private.example/video"}`,
		`{"source_message_id":"share-one","offset":-1}`,
		`{"source_message_id":"share-one","timeout_seconds":0}`,
		`{"source_message_id":"share-one"}{"source_message_id":"two"}`,
	} {
		if _, err := decodeShareServiceArguments(json.RawMessage(raw)); err == nil {
			t.Fatal("unsafe share argument accepted", raw)
		}
	}
}

func TestShareServiceChannelsSchemaRejectsUnsupportedPaging(t *testing.T) {
	cap := shareServiceCapability("share.resolve_channels")
	if strings.Contains(string(cap.InputSchema), "candidate_offset") || strings.Contains(string(cap.InputSchema), `"offset"`) {
		t.Fatal("channels advertised pagination it cannot execute", string(cap.InputSchema))
	}
	if !shareServiceHasPaging(json.RawMessage(`{"source_message_id":"share-one","offset":0}`)) || shareServiceHasPaging(json.RawMessage(`{"source_message_id":"share-one","timeout_seconds":45}`)) {
		t.Fatal("channels paging boundary changed")
	}
}

func TestShareServiceSourceRereadsAndKeepsConversationScope(t *testing.T) {
	f, p := trustedAgent(t)
	kind := 4
	n := &bindingChannelsNative{fakeNative: f.native, msg: linuxnative.Message{ID: "share-one", AccountID: "test", ThreadID: "wxid_owner", SenderID: "wxid_sender", Direction: "incoming", Type: 49, AppMessageType: &kind, Link: &linuxnative.LinkCard{URL: "https://public.example/work?private=discarded"}}}
	f.manager.native = n
	message, err := p.shareServiceSource(context.Background(), "share-one")
	if err != nil || message.ID != "share-one" {
		t.Fatal("authorized canonical share rejected", err)
	}
	n.msg.ThreadID = "another-room"
	if _, err := p.shareServiceSource(context.Background(), "share-one"); err == nil {
		t.Fatal("cross-conversation source accepted")
	}
	n.msg.ThreadID = "wxid_owner"
	n.msg.Direction = "outgoing"
	if _, err := p.shareServiceSource(context.Background(), "share-one"); err == nil {
		t.Fatal("outgoing source accepted")
	}
}

func TestShareFacadeProjectionNeverLeaksResolverOrClaimsObservation(t *testing.T) {
	kind := 4
	message := linuxnative.Message{ID: "share-one", Type: 49, AppMessageType: &kind, Link: &linuxnative.LinkCard{Title: "Card title"}}
	no := false
	public := publicShareObservation{Title: "Provider title", SourceURL: "https://private.example/page?token=secret", Text: "甲乙丙丁", Locators: []string{"https://private.example/video?token=secret"}, Coverage: "media_locator_only", Observed: &no, Downloaded: &no}
	result := publicShareFacadeResult(message, public, shareServiceArguments{Limit: 2, CandidateLimit: 1})
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private.example") || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "source_url") || strings.Contains(string(raw), "locator") {
		t.Fatal("private resolver data leaked", string(raw))
	}
	if result["video_observed"] != false || result["media_downloaded"] != false || result["text"] != "甲乙" || result["total_characters"] != 4 {
		t.Fatal("unsafe or broken public projection", string(raw))
	}
	if result["effective_timeout_seconds"] != int64(30) {
		t.Fatal("public effective timeout omitted", string(raw))
	}
	channels := channelsShareFacadeResult(message, linuxnative.ChannelsSource{ObjectID: "opaque-id", MediaURL: "https://private.example/video?decode=secret", DecodeKey: "secret", Title: "Channels title"}, shareServiceArguments{Limit: 20, CandidateLimit: 1})
	raw, _ = json.Marshal(channels)
	if strings.Contains(string(raw), "opaque") || strings.Contains(string(raw), "private.example") || strings.Contains(string(raw), "secret") || channels["video_observed"] != false {
		t.Fatal("channels private handoff leaked", string(raw))
	}
}

func TestShareServiceAvailabilityDoesNotRequireBridgeConfiguration(t *testing.T) {
	f, p := trustedAgent(t)
	_, store, _, _ := shareBindingFixtureFor(t, "test", shareServicePluginID, true)
	f.manager.packages = store
	if !p.shareServiceCapabilityAvailable(context.Background()) {
		t.Fatal("installed share reader hidden without bridge credentials")
	}
	// This is only a discovery test. No controller, bridge, remote service, or
	// real WeChat call is attempted.
}

func TestExplicitShareActorIsDistinctFromUploaderAndRevokesBeforeProvider(t *testing.T) {
	f, native, _, ctx := receivedPolicyFixture(t, false)
	access, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	access.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: native.msg.ThreadID, Sender: "alice", TraceID: "alice-explicit-read", Mode: "direct"})
	actorCtx := withSourceActor(ctx, access, agentID("share-test", "alice-request"), native.msg.ThreadID)
	policy, err := f.manager.newReceivedSharePolicy(actorCtx, native.msg)
	if err != nil {
		t.Fatal(err)
	}
	inv := policy.invocation()
	if inv.Requester != "alice" || inv.OriginSourceSender != native.msg.SenderID || inv.RequestThread != native.msg.ThreadID || inv.Requester == inv.OriginSourceSender {
		t.Fatalf("actor/source provenance collapsed: %+v", inv)
	}
	// Change the authenticated tool turn before any provider callback. Check
	// must fail at this point, so no request or source data can be released.
	access.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: native.msg.ThreadID, Sender: "mallory", TraceID: "mallory-turn", Mode: "direct", MessageTime: time.Now().UTC().Format(time.RFC3339Nano)})
	if err := policy.Check(actorCtx, pluginhost.Job{Plugin: "share-reader", Tool: "share.resolve_channels", Context: inv}, pluginhost.Capability{}); err == nil {
		t.Fatal("revoked actor passed source policy check")
	}
}
