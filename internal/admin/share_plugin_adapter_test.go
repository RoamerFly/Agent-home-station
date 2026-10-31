package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

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
