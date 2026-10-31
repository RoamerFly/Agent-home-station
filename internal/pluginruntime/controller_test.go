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
