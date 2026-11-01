package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

type startFunc func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error)

func (f startFunc) Start(c context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginhost.Session, error) {
	return f(c, p, g)
}

func installedFixture(t *testing.T) (*pluginhost.Store, pluginhost.InstalledPackage) {
	t.Helper()
	ctx := context.Background()
	source := t.TempDir()
	readyFixture(t, source)
	s, err := pluginhost.OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := s.Prepare(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(ctx, p.Token, p.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, i
}

func TestControllerFailedStartupReleasesReservationAndRejectsStaleState(t *testing.T) {
	s, i := installedFixture(t)
	ctx := context.Background()
	calls := 0
	c, err := NewController(s, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		calls++
		if _, e := s.SaveConfiguration(ctx, i.Package.ID, pluginhost.ConfigUpdate{}); e == nil {
			t.Fatal("editing allowed during startup")
		}
		return nil, errors.New("synthetic startup failure")
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Start(ctx, i.Package.ID, i.Revision+1) == nil || calls != 0 {
		t.Fatal("stale state started plugin")
	}
	if c.Start(ctx, i.Package.ID, i.Revision) == nil || calls != 1 {
		t.Fatal("startup failure not returned")
	}
	now, err := s.Installed(ctx, i.Package.ID)
	if err != nil || now.Enabled || now.Revision != 3 {
		t.Fatal("startup reservation not released", now, err)
	}
	if c.Status(i.Package.ID).State != "failed" {
		t.Fatal("startup failure hidden")
	}
	if _, err = s.SaveConfiguration(ctx, i.Package.ID, pluginhost.ConfigUpdate{}); err != nil {
		t.Fatal(err)
	}
	if err = c.Stop(ctx, i.Package.ID, now.Revision); err != nil {
		t.Fatal(err)
	}
	if c.Status(i.Package.ID).State != "stopped" {
		t.Fatal("stopped status not cleared")
	}
}

func TestControllerStartAcceptsHostJSONCloneOfOptionalManifestCollections(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	readyFixture(t, source)
	manifestPath := filepath.Join(source, "plugin.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	delete(manifest, "dependencies")
	raw, err = json.Marshal(manifest)
	if err != nil || os.WriteFile(manifestPath, raw, 0600) != nil {
		t.Fatal("cannot omit optional dependencies from fixture")
	}
	store, err := pluginhost.OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepared, err := store.Prepare(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(ctx, prepared.Token, prepared.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Package.Dependencies != nil {
		t.Fatal("fixture must omit optional dependencies")
	}
	resolver := InstalledExecutor(store, Options{})
	started := false
	controller, err := NewController(store, startFunc(func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginhost.Session, error) {
		// Controller.Start reaches this through Host.Register, whose defensive
		// JSON clone represents omitted optional collections as empty ones.
		if p.Dependencies == nil {
			t.Fatal("host registration did not exercise JSON clone")
		}
		if _, err := resolver.Resolve(ctx, p, g); err != nil {
			return nil, err
		}
		started = true
		return &invocationSession{call: func(pluginhost.Invocation, string, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if err = controller.Start(ctx, installed.Package.ID, installed.Revision); err != nil {
		t.Fatalf("equivalent host registration was rejected: %v", err)
	}
	if !started || controller.Status(installed.Package.ID).State != "running" {
		t.Fatal("controller did not start cloned package")
	}
	current, err := store.Installed(ctx, installed.Package.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.Stop(ctx, current.Package.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestLiveInstalledControllerConfigurationAndRestart(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated installed-package test")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	s, i := installedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := s.SaveConfiguration(ctx, i.Package.ID, pluginhost.ConfigUpdate{Secrets: map[string]string{"api_key": "synthetic-plugin-only-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}
	c, err := NewController(s, InstalledExecutor(s, options))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Start(ctx, i.Package.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	if c.Status(i.Package.ID).State != "running" {
		t.Fatal("session not running")
	}
	raw, err := c.host.Call(ctx, i.Package.ID, invocation(), "configuration", json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var received pluginhost.RuntimeConfiguration
	if json.Unmarshal(raw, &received) != nil || received.Revision != config.Revision || string(received.Values["api_key"]) != `"synthetic-plugin-only-secret"` {
		t.Fatal("private configuration not delivered to approved process")
	}
	public, err := s.Configuration(ctx, i.Package.ID)
	if err != nil || len(public.ConfiguredSecrets) != 1 || public.Values["api_key"] != nil {
		t.Fatal("secret in public response")
	}
	if _, err = s.SaveConfiguration(ctx, i.Package.ID, pluginhost.ConfigUpdate{Revision: config.Revision}); err == nil {
		t.Fatal("live configuration edited")
	}
	request := CallRequest{Plugin: i.Package.ID, Capability: "echo", Context: invocation(), Arguments: json.RawMessage(`{"text":"隔离调用合成结果"}`)}
	completed, err := c.Invoke(ctx, request, invocationPolicy{})
	if err != nil || completed.Job.State != "completed" || len(completed.Job.Result) == 0 {
		t.Fatal("isolated call did not checkpoint", completed, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := NewController(s, InstalledExecutor(s, options))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	i, err = s.Installed(ctx, i.Package.ID)
	if err != nil || !i.Enabled {
		t.Fatal("administrative intent lost")
	}
	if next.Status(i.Package.ID).State != "stopped" {
		t.Fatal("restart automatically ran process")
	}
	if err = next.Start(ctx, i.Package.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	reused, err := next.Invoke(ctx, request, invocationPolicy{validate: func(pluginhost.Job, json.RawMessage) (VerifiedResult, error) {
		t.Fatal("completed request was executed after restart")
		return VerifiedResult{}, nil
	}})
	if err != nil || !reused.Reused || reused.Job.ID != completed.Job.ID || string(reused.Job.Result) != string(completed.Job.Result) {
		t.Fatal("durable result not recovered", reused, err)
	}
	i, _ = s.Installed(ctx, i.Package.ID)
	if err = next.Stop(ctx, i.Package.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	i, _ = s.Installed(ctx, i.Package.ID)
	if i.Enabled || next.Status(i.Package.ID).State != "stopped" {
		t.Fatal("stop not persisted")
	}
}
