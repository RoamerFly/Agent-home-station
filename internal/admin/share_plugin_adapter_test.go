package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

type shareFailureExecutor struct{}

func (e shareFailureExecutor) Start(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
	return e, nil
}
func (shareFailureExecutor) Health(ctx context.Context) error { return ctx.Err() }
func (shareFailureExecutor) Close() error                     { return nil }
func (shareFailureExecutor) Call(ctx context.Context, _ pluginhost.Invocation, _ string, _ json.RawMessage) (json.RawMessage, error) {
	_, err := pluginruntime.InvokeHostService(ctx, "wechat/http_route", routeEnvelope("channels-parse", parseBody, ""))
	if err != nil {
		// The packaged stdio entry point deliberately returns this fixed code.
		return json.RawMessage(`{"ok":false,"error":"host_service_denied_or_failed"}`), nil
	}
	return json.RawMessage(`{"ok":false,"error":"untrusted_plugin_error_with_secret"}`), nil
}

func TestReceivedShareFailureKeepsHostStageWithoutReplayingUnknownJob(t *testing.T) {
	for _, mode := range []string{"http", "network", "provider", "invalid_response", "source_changed", "arbitrary_error", "unreviewed_web_code", "plugin_error"} {
		t.Run(mode, func(t *testing.T) {
			f, native, policy, ctx := receivedPolicyFixture(t, false)
			calls := 0
			policy.send = func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
				calls++
				switch mode {
				case "http":
					return nil, &mcptools.WebError{Code: "plugin_route_http_rejected", Status: 403, Stage: "http_status"}
				case "network":
					return nil, &mcptools.WebError{Code: "plugin_route_network_failed", Stage: "connection_or_headers"}
				case "provider":
					return json.RawMessage(`{"code":-1007,"message":"synthetic-private-token https://private.invalid/secret"}`), nil
				case "source_changed":
					native.msg.Body = "edited source"
					return json.RawMessage(`{"code":0}`), nil
				case "plugin_error":
					return json.RawMessage(`{"code":0,"data":{"playable_url":"` + previewURL + `"}}`), nil
				case "unreviewed_web_code":
					return nil, &mcptools.WebError{Code: "synthetic-private-token https://private.invalid/secret", Status: 403}
				case "arbitrary_error":
					return nil, errors.New("synthetic-private-token https://private.invalid/secret")
				default:
					return json.RawMessage(`{"code":0,"data":{"playable_url":"https://private.invalid/secret"}}`), nil
				}
			}
			controller, err := pluginruntime.NewController(policy.store, shareFailureExecutor{})
			if err != nil {
				t.Fatal(err)
			}
			f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
			_, err = policy.resolve(ctx, native.source)
			var diagnostic *shareResolutionFailure
			stage := "channels_parse"
			if mode == "plugin_error" {
				stage = "result_validation"
			}
			if !errors.As(err, &diagnostic) || diagnostic.stage != stage || calls != 1 {
				t.Fatalf("lost host diagnostic: %v, calls=%d", err, calls)
			}
			want := map[string]string{"http": "share_plugin_route_http_rejected", "network": "share_plugin_route_network_failed", "provider": "share_route_provider_rejected", "invalid_response": "share_route_response_binding_failed", "source_changed": "share_native_source_changed", "arbitrary_error": "share_plugin_resolution_incomplete", "unreviewed_web_code": "share_plugin_resolution_incomplete", "plugin_error": "share_result_binding_failed"}[mode]
			if diagnostic.code != want || sharedVideoReadReason(err) != want {
				t.Fatalf("wrong safe cause: %+v", diagnostic)
			}
			if mode == "http" && diagnostic.status != 403 || mode == "provider" && (diagnostic.provider == nil || *diagnostic.provider != -1007) {
				t.Fatal("host status or numeric provider code was lost")
			}
			page, err := policy.store.Jobs(ctx, "share-reader", 0, 10)
			rows := page.Items
			if err != nil || len(rows) != 1 || rows[0].State != "needs_review" || len(rows[0].Result) != 0 {
				t.Fatal("unknown resolver result was promoted", rows, err)
			}
			if _, err = policy.store.PrivateJobResult(ctx, rows[0], func(context.Context, pluginhost.Job) error { return nil }); err == nil {
				t.Fatal("failed resolver acquired a private handoff")
			}
			native.msg.Body = ""
			if _, err = policy.resolve(ctx, native.source); err == nil || calls != 1 {
				t.Fatal("unknown job was retried", err, calls)
			}
			f.manager.mu.Lock()
			journal, _ := json.Marshal(f.manager.journal)
			f.manager.mu.Unlock()
			if strings.Contains(string(journal), "synthetic-private-token") || strings.Contains(string(journal), "private.invalid") || strings.Contains(string(journal), "untrusted_plugin_error") {
				t.Fatal("unsafe error reached journal")
			}
			if !strings.Contains(string(journal), `"tool_stage":"`+stage+`"`) || !strings.Contains(string(journal), want) {
				t.Fatal("safe diagnostic was not persisted")
			}
		})
	}
}

type receivedPluginNative struct{ *bindingChannelsNative }

func (n *receivedPluginNative) ChannelsShareURL(_ context.Context, id, thread, object string) (string, error) {
	if id != n.msg.ID || thread != n.msg.ThreadID || object != n.source.ObjectID {
		return "", errors.New("wrong native shortlink request")
	}
	return "https://weixin.qq.com/sph/synthetic123", nil
}

func receivedPolicyFixture(t *testing.T, remember bool) (*webFixture, *bindingChannelsNative, *receivedSharePolicy, context.Context) {
	return receivedPolicyFixtureFor(t, remember, 51)
}

func receivedPolicyFixtureFor(t *testing.T, remember bool, kind int) (*webFixture, *bindingChannelsNative, *receivedSharePolicy, context.Context) {
	t.Helper()
	f := setup(t)
	_, store, _, _ := shareBindingFixtureFor(t, "test", "share-reader", true)
	f.manager.packages = store
	f.manager.config.Enabled = true
	f.manager.config.Behavior.ReadVideos = true
	f.manager.config.Companion.Permissions.Remember = remember
	n := &bindingChannelsNative{fakeNative: f.native, msg: linuxnative.Message{ID: "message", AccountID: "test", ThreadID: "filehelper", SenderID: "synthetic-user", ServerID: "server-synthetic", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Direction: "incoming", Type: 49, AppMessageType: &kind}, source: linuxnative.ChannelsSource{AccountID: "test", ThreadID: "filehelper", MessageID: "message", ObjectID: "12345"}}
	tool := "share.resolve_channels"
	if kind != 51 {
		tool = "share.resolve_public"
		n.msg.Link = &linuxnative.LinkCard{URL: "https://public.example/share?key=synthetic", Title: "synthetic share"}
		f.manager.mcp.Enabled, f.manager.mcp.WebFetchEnabled = true, true
	}
	f.manager.native = &receivedPluginNative{n}
	ctx := t.Context()
	if remember {
		original := ""
		in := companion.Input{Channel: "wechat", Account: "test", Thread: n.msg.ThreadID, Sender: n.msg.SenderID, TraceID: n.msg.ID, MessageTime: n.msg.CreatedAt, OriginalText: &original, Text: "synthetic shared video", Source: &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: n.msg.ID, ServerID: n.msg.ServerID, Account: "test", Thread: n.msg.ThreadID, Sender: n.msg.SenderID, MessageType: 49, OccurredAt: n.msg.CreatedAt, Revision: 1}}
		if err := f.manager.memory.Observe(in, f.manager.config.Companion, time.Now()); err != nil {
			t.Fatal(err)
		}
		ref, ok := f.manager.memory.SourceForInput(in)
		if !ok {
			t.Fatal("source not persisted")
		}
		ctx = companion.WithMediaSource(ctx, *ref)
	}
	p, err := f.manager.newReceivedSharePolicyFor(ctx, n.msg, tool)
	if err != nil {
		t.Fatal(err)
	}
	return f, n, p, ctx
}

func TestReceivedSharePolicyScopeRevocationAndNoMemory(t *testing.T) {
	for _, remember := range []bool{false, true} {
		t.Run(map[bool]string{false: "ephemeral", true: "remembered"}[remember], func(t *testing.T) {
			f, n, p, ctx := receivedPolicyFixture(t, remember)
			j := pluginhost.Job{Plugin: "share-reader", Tool: "share.resolve_channels", Context: p.invocation()}
			if err := p.Check(ctx, j, pluginhost.Capability{}); err != nil {
				t.Fatal(err)
			}
			foreign := j
			foreign.Context.Thread = "another-room"
			if err := p.Check(ctx, foreign, pluginhost.Capability{}); err == nil {
				t.Fatal("cross room accepted")
			}
			n.msg.Body = "edited native card"
			if err := p.Check(ctx, j, pluginhost.Capability{}); err == nil {
				t.Fatal("native edit accepted")
			}
			n.msg.Body = ""
			f.manager.config.Behavior.ReadVideos = false
			if err := p.Check(ctx, j, pluginhost.Capability{}); err == nil {
				t.Fatal("read permission revoked but accepted")
			}
			f.manager.config.Behavior.ReadVideos = true
			if remember {
				if err := f.manager.memory.Clear(companion.ScopeID("wechat", "test", "filehelper")); err != nil {
					t.Fatal(err)
				}
				if err := p.Check(ctx, j, pluginhost.Capability{}); err == nil {
					t.Fatal("forgotten source accepted")
				}
			} else {
				if _, err := f.manager.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", "test", "filehelper")}, p.source.EventID); err == nil {
					t.Fatal("ephemeral media written to memory")
				}
			}
		})
	}
}

func TestReceivedShareReadOnlySnapshotIdentity(t *testing.T) {
	_, _, policy, ctx := receivedPolicyFixture(t, false)
	installed, err := policy.store.Installed(ctx, "share-reader")
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := policy.store.Configuration(ctx, "share-reader")
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"url":"https://weixin.qq.com/sph/synthetic123","expected_object_id":"12345"}`)
	policy.setReadOnlyResolutionSnapshot(args, installed, configuration)
	first := policy.invocation().RequestKey
	policy.setReadOnlyResolutionSnapshot(args, installed, configuration)
	if got := policy.invocation().RequestKey; got != first {
		t.Fatal("identical read-only request did not deduplicate")
	}
	changedArgs := json.RawMessage(`{"url":"https://weixin.qq.com/sph/synthetic456","expected_object_id":"12345"}`)
	policy.setReadOnlyResolutionSnapshot(changedArgs, installed, configuration)
	if got := policy.invocation().RequestKey; got == first {
		t.Fatal("changed parser arguments reused the old immutable request key")
	}
	policy.setReadOnlyResolutionSnapshot(args, installed, pluginhost.Configuration{Revision: configuration.Revision + 1})
	if got := policy.invocation().RequestKey; got == first {
		t.Fatal("changed plugin configuration reused the old immutable request key")
	}
	policy.epoch += "-changed"
	policy.setReadOnlyResolutionSnapshot(args, installed, configuration)
	if got := policy.invocation().RequestKey; got == first {
		t.Fatal("changed recall epoch collided with the previous completed read")
	}
}

func TestShareRequestConflictIsSafeDiagnostic(t *testing.T) {
	failure := safeShareResolutionFailure("plugin_invoke", pluginhost.ErrRequestConflict)
	if failure.code != "share_request_conflict" || failure.stage != "plugin_invoke" {
		t.Fatalf("request conflict was not retained as a safe diagnostic: %+v", failure)
	}
}

func TestReceivedShareDisabledPluginDoesNotFallBackToLegacy(t *testing.T) {
	f, n, p, ctx := receivedPolicyFixture(t, false)
	i, err := p.store.Installed(ctx, "share-reader")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.store.SetEnabled(ctx, "share-reader", i.Revision, false); err != nil {
		t.Fatal(err)
	}
	_, err = f.manager.resolveReceivedChannels(ctx, n.msg, n.source)
	if err == nil || err.Error() != "share_plugin_disabled" {
		t.Fatal("disabled plugin used old configuration", err)
	}
}

func TestLiveReceivedSharePolicyIsolatedExecutionAndRecovery(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in real isolated Python; synthetic HTTP only")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	for _, remember := range []bool{false, true} {
		t.Run(map[bool]string{false: "ephemeral", true: "remembered"}[remember], func(t *testing.T) {
			f, n, p, ctx := receivedPolicyFixture(t, remember)
			var err error
			p.binding, err = newShareRouteBinding(n.msg, n.source, p.source, "https://weixin.qq.com/sph/synthetic123")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			p.send = func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
				calls++
				if calls == 1 {
					return json.RawMessage(`{"code":0,"data":{"playable_url":"` + previewURL + `"}}`), nil
				}
				return json.RawMessage(verifiedFeed), nil
			}
			controller, err := pluginruntime.NewController(p.store, pluginruntime.InstalledExecutor(p.store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
			i, err := p.store.Installed(ctx, "share-reader")
			if err != nil {
				t.Fatal(err)
			}
			if err = controller.Start(ctx, i.Package.ID, i.Revision); err != nil {
				t.Fatal(err)
			}
			resolved, err := p.resolve(ctx, n.source)
			if err != nil || calls != 2 || resolved.MessageID != n.msg.ID || resolved.DecodeKey != "18446744073709551615" {
				t.Fatal("actual host adapter failed", calls, err)
			}
			again, err := p.resolve(ctx, n.source)
			if err != nil || calls != 2 || again != resolved {
				t.Fatal("private handoff lost identity")
			}
		})
	}
}

func TestLiveReceivedShareModernFeedUsesExportIdentityAndPrivateHandoff(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated Python; anonymous provider responses only")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	f, native, policy, ctx := receivedPolicyFixture(t, true)
	native.source.Title, native.source.Author = "Anonymous video", "Anonymous author"
	calls := 0
	policy.send = func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return json.RawMessage(`{"code":0,"data":{"playable_url":"` + previewURL + `","wx_export_id":"export/not-the-observed-eid"}}`), nil
		}
		return json.RawMessage(`{"errCode":0,"data":{"authorInfo":{"nickname":"Anonymous author"},"feedInfo":{"description":"Anonymous video","videoUrl":"https://media.example/anonymous.mp4"},"sceneInfo":{"dynamicExportId":"export/synthetic"}}}`), nil
	}
	controller, err := pluginruntime.NewController(policy.store, pluginruntime.InstalledExecutor(policy.store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	resolved, err := policy.resolve(ctx, native.source)
	if err != nil || calls != 2 || resolved.ObjectID != native.source.ObjectID || resolved.DecodeKey != "" || resolved.MediaURL != "https://media.example/anonymous.mp4" {
		t.Fatal("isolated modern feed contract failed", calls, err)
	}
	again, err := policy.resolve(ctx, native.source)
	if err != nil || again != resolved || calls != 2 {
		t.Fatal("completed modern source was replayed", err, calls)
	}
}
