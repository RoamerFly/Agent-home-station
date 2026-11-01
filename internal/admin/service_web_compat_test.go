package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func TestWebServiceCapabilityMapsAudioAliasToInstalledSearch(t *testing.T) {
	if got := webServiceCapability("web_audio_search"); got != "web.search" {
		t.Fatalf("audio alias capability=%q", got)
	}
	if got := webServiceCapability("web_fetch"); got != "web.read" {
		t.Fatalf("fetch capability=%q", got)
	}
}

func installPublicWebFixture(t *testing.T, f *webFixture) (*pluginhost.Store, pluginhost.InstalledPackage) {
	t.Helper()
	s, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Prepare(t.Context(), "../../plugins/web-search")
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(t.Context(), p.Token, p.Digest, []string{"network"})
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.SetEnabled(t.Context(), i.Package.ID, i.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := pluginruntime.NewController(s, &packageTestExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = runtime, true
	return s, i
}

func TestWebPluginAliasesRequireExplicitMigrationForEveryOldBackend(t *testing.T) {
	for _, backend := range []string{"public", "deepseek", "openai-compatible"} {
		t.Run(backend, func(t *testing.T) {
			f, p := trustedAgent(t)
			f.manager.config.Provider = backend
			f.manager.config.APIKey = "synthetic-old-model-key"
			// A nil Hub makes any accidental built-in dispatch fail immediately.
			p.hub = nil
			for _, name := range []string{"web_search", "web_audio_search", "web_fetch", "web_discover"} {
				_, err := p.Call(t.Context(), name, json.RawMessage(`{"query":"synthetic","url":"https://must-not-resolve.invalid/"}`))
				var web *mcptools.WebError
				if !errors.As(err, &web) || web.Code != "web_plugin_migration_required" || p.enabled(name) {
					t.Fatal("uninstalled public tool reached legacy backend", name, err)
				}
				if description := mcptools.FailureDescription(err); !strings.Contains(description, "web_plugin_migration_required") || strings.Contains(description, "synthetic-old-model-key") {
					t.Fatal("migration requirement hidden or leaked legacy credentials", description)
				}
			}
		})
	}
}

func TestWebPluginAliasesDoNotFallBackWhenDisabledRemovedOrMisconfigured(t *testing.T) {
	for _, state := range []string{"disabled", "removed", "misconfigured", "runtime_unavailable"} {
		t.Run(state, func(t *testing.T) {
			f, p := trustedAgent(t)
			s, i := installPublicWebFixture(t, f)
			p.hub = nil
			executor := &packageTestExecutor{}
			runtime, err := pluginruntime.NewController(s, executor)
			if err != nil {
				t.Fatal(err)
			}
			f.manager.packageRuntime, f.manager.packageRuntimeReady = runtime, state != "runtime_unavailable"
			switch state {
			case "disabled", "removed", "misconfigured":
				i, err = s.SetEnabled(t.Context(), i.Package.ID, i.Revision, false)
				if err != nil {
					t.Fatal(err)
				}
				if state == "removed" {
					db, openErr := sql.Open("sqlite", filepath.Join(f.dir, "plugin-host", "plugins.sqlite"))
					if openErr != nil {
						t.Fatal(openErr)
					}
					_, err = db.ExecContext(t.Context(), `UPDATE installed SET removed=1 WHERE id=?`, i.Package.ID)
					db.Close()
				} else if state == "misconfigured" {
					_, err = s.SaveConfiguration(t.Context(), i.Package.ID, pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"search_backend": json.RawMessage(`"deepseek"`)}})
					if err == nil {
						i, err = s.SetEnabled(t.Context(), i.Package.ID, i.Revision, true)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"web_search", "web_audio_search"} {
				if result, err := p.Call(context.Background(), name, json.RawMessage(`{"query":"synthetic"}`)); err == nil || result != "" {
					t.Fatal("unavailable plugin fell through", state, name, result, err)
				}
			}
			if executor.calls != 0 {
				t.Fatal("unavailable plugin executed", state, executor.calls)
			}
		})
	}
}
