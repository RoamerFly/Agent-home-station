package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// TestLiveReceivedChannelsBridgeOfficialPackage exercises the complete
// official share-reader protocol in an isolated Python process.  Its only
// network endpoint is a loopback httptest bridge; it sends no WeChat traffic
// and uses no model/provider credentials.
func TestLiveReceivedChannelsBridgeOfficialPackage(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in real isolated Python; loopback bridge only")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	f, native, p, ctx := receivedPolicyFixture(t, false)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/channels/feed/profile" || r.URL.Query().Get("object_id") != native.source.ObjectID || r.URL.Query().Get("nonce_id") != native.source.ObjectNonceID {
			http.Error(w, "unexpected bridge request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(syntheticBridgeFeed))
	}))
	defer server.Close()
	// Configure the official package while stopped. configureReceivedBridge
	// creates a direct-test binding too; resolve must create its own binding.
	_ = configureReceivedBridge(t, p, ctx, server.URL)
	p.binding = nil

	controller, err := pluginruntime.NewController(p.store, pluginruntime.InstalledExecutor(p.store, pluginruntime.Options{
		Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)},
	}))
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

	resolved, err := p.resolve(ctx, native.source)
	if err != nil || calls != 1 || resolved.ObjectID != native.source.ObjectID || resolved.MediaURL != "https://video.example.test/object.mp4" || resolved.DecodeKey != "18446744073709551615" {
		if jobs, jobsErr := p.store.Jobs(ctx, "share-reader", 0, 10); jobsErr == nil {
			t.Logf("bridge jobs after failure: %#v", jobs.Items)
		}
		t.Fatalf("official isolated bridge failed: calls=%d resolved=%#v err=%v", calls, resolved, err)
	}
	// The idempotency key is unchanged. A retry must use the private verified
	// checkpoint, not contact the bridge or submit a second execution.
	again, err := p.resolve(ctx, native.source)
	if err != nil || calls != 1 || again != resolved {
		t.Fatalf("bridge retry was not recovered from checkpoint: calls=%d result=%#v err=%v", calls, again, err)
	}

	foreign := native.source
	foreign.ObjectID = "99999"
	if _, err = p.resolve(ctx, foreign); err == nil || calls != 1 {
		t.Fatalf("foreign native source reached bridge: calls=%d err=%v", calls, err)
	}

	// Configuration edits require a stopped process; after the revision changes
	// the stale route cannot be resumed through its prior private checkpoint.
	i, err = p.store.Installed(ctx, "share-reader")
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.Stop(ctx, i.Package.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	configuration, err := p.store.Configuration(ctx, "share-reader")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.store.SaveConfiguration(ctx, "share-reader", pluginhost.ConfigUpdate{Revision: configuration.Revision, Values: map[string]json.RawMessage{"object_bridge_url": json.RawMessage(`"http://127.0.0.1:1"`)}, Secrets: map[string]string{"yuanbao_cookie": "synthetic=secret"}}); err != nil {
		t.Fatal(err)
	}
	i, err = p.store.Installed(ctx, "share-reader")
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.Start(ctx, i.Package.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = p.resolve(ctx, linuxnative.ChannelsSource(native.source)); err == nil || calls != 1 {
		t.Fatalf("changed configuration resumed old bridge checkpoint: calls=%d err=%v", calls, err)
	}
}
