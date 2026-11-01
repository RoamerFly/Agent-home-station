package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

type musicServiceFixtureExecutor struct{ calls *int }

func (e musicServiceFixtureExecutor) Start(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
	return e, nil
}
func (e musicServiceFixtureExecutor) Health(ctx context.Context) error { return ctx.Err() }
func (e musicServiceFixtureExecutor) Close() error                     { return nil }
func (e musicServiceFixtureExecutor) Call(ctx context.Context, _ pluginhost.Invocation, tool string, args json.RawMessage) (json.RawMessage, error) {
	if tool != musicServiceControlCapability {
		return nil, errors.New("unexpected fixture tool")
	}
	*e.calls++
	return pluginruntime.InvokeHostService(ctx, "wechat/service_control", args)
}

func musicServiceFixture(t *testing.T) (*webFixture, *permittedTools, *pluginhost.Store) {
	t.Helper()
	f, p, store := localMusicPluginFixture(t, "http://127.0.0.1:6188")
	config := f.manager.agent.config()
	config.OwnerIDs = []string{p.trace.Sender}
	config.PrivilegedThreads = []string{p.trace.Thread}
	if err := f.manager.agent.save(config); err != nil {
		t.Fatal(err)
	}
	return f, p, store
}

func serviceControlJob(t *testing.T, p *musicServiceControlPolicy) (pluginhost.Job, pluginhost.Capability) {
	t.Helper()
	j, _, err := p.base.store.CreateJob(t.Context(), pluginhost.Job{Plugin: "music", Tool: musicServiceControlCapability, Version: p.base.version, PackageDigest: p.base.digest, ConfigRevision: strconv.Itoa(p.base.configRevision), Context: p.base.invocation, Arguments: p.arguments})
	if err != nil {
		t.Fatal(err)
	}
	j, err = p.base.store.ClaimJob(t.Context(), j.ID, j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return j, pluginhost.Capability{ID: musicServiceControlCapability, Permissions: []string{"network"}, Implementation: "ready"}
}

func TestMusicServiceInstalledCapabilityControllerAndExactReply(t *testing.T) {
	f, p, store := musicServiceFixture(t)
	calls := 0
	c, err := pluginruntime.NewController(store, musicServiceFixtureExecutor{calls: &calls})
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageMu.Lock()
	f.manager.packageRuntime, f.manager.packageRuntimeReady = c, true
	f.manager.packageMu.Unlock()
	out, err := p.controlMusicService(t.Context(), "status")
	if err != nil || calls != 1 || !strings.Contains(out, `"coverage":"owned_supervisor_only"`) || !strings.Contains(out, `"running":false`) {
		t.Fatal(out, err, calls)
	}
	policy, err := p.newMusicServiceControlPolicy(t.Context(), "status")
	if err != nil {
		t.Fatal(err)
	}
	j, cap := serviceControlJob(t, policy)
	if _, err := policy.Service(t.Context(), j, cap, "wechat/service_control", json.RawMessage(`{"action":"status","argv":["unsafe"]}`)); err == nil {
		t.Fatal("plugin-supplied launch arguments accepted")
	}
	reply, err := policy.Service(t.Context(), j, cap, "wechat/service_control", policy.arguments)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Service(t.Context(), j, cap, "wechat/service_control", policy.arguments); err == nil {
		t.Fatal("replayed callback")
	}
	if _, err := policy.Validate(t.Context(), j, cap, json.RawMessage(`{"running":true}`)); err == nil {
		t.Fatal("fabricated running state accepted")
	}
	if _, err := policy.Validate(t.Context(), j, cap, reply); err != nil {
		t.Fatal(err)
	}
	// The durable job retains the lifecycle capability, not the reused health
	// check's bridge capability. The policy must reject a mismatched tool.
	j.Tool = localMusicCapability
	if err := policy.Check(t.Context(), j, cap); err == nil {
		t.Fatal("job capability substitution accepted")
	}
}

func TestMusicServicePolicyRevokesOwnerAdminSourceConfigurationPackageAndCancel(t *testing.T) {
	for _, kind := range []string{"owner", "admin", "source", "configuration", "disabled", "cancelled", "package"} {
		t.Run(kind, func(t *testing.T) {
			f, p, store := musicServiceFixture(t)
			ctx := t.Context()
			validAdmin := true
			if kind == "admin" {
				ctx = context.WithValue(ctx, speechAdminTestContextKey{}, &speechAdminTestAuthority{Account: f.manager.native.Account(), Session: "synthetic", RequestID: t.Name(), Check: func(context.Context) error {
					if !validAdmin {
						return errors.New("revoked")
					}
					return nil
				}})
			}
			policy, err := p.newMusicServiceControlPolicy(ctx, "status")
			if err != nil {
				t.Fatal(err)
			}
			j, cap := serviceControlJob(t, policy)
			switch kind {
			case "owner":
				config := f.manager.agent.config()
				config.OwnerIDs = nil
				if err := f.manager.agent.save(config); err != nil {
					t.Fatal(err)
				}
			case "admin":
				validAdmin = false
			case "source":
				p.trace.Source.Revision++
			case "configuration":
				i, _ := store.Installed(t.Context(), "music")
				i, err = store.SetEnabled(t.Context(), "music", i.Revision, false)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.SaveConfiguration(t.Context(), "music", pluginhost.ConfigUpdate{Revision: policy.base.configRevision, Secrets: map[string]string{"local_token": "changed-synthetic-token"}}); err != nil {
					t.Fatal(err)
				}
				i, err = store.Installed(t.Context(), "music")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.SetEnabled(t.Context(), "music", i.Revision, true); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				i, _ := store.Installed(t.Context(), "music")
				if _, err := store.SetEnabled(t.Context(), "music", i.Revision, false); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if _, err := store.RequestJobCancel(t.Context(), j.ID, j.Revision); err != nil {
					t.Fatal(err)
				}
			case "package":
				dir, err := store.PackageDirectory(policy.base.digest)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, "runtime", "main.py"), []byte("# synthetic package tampering"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := policy.Service(ctx, j, cap, "wechat/service_control", policy.arguments); err == nil {
				t.Fatal("revoked lifecycle callback executed")
			}
			if policy.requested {
				t.Fatal("revoked callback reached execution")
			}
		})
	}
}

func TestMusicServiceOwnerAndCurrentPluginRequired(t *testing.T) {
	f, p, _ := musicServiceFixture(t)
	config := f.manager.agent.config()
	config.OwnerIDs = nil
	if err := f.manager.agent.save(config); err != nil {
		t.Fatal(err)
	}
	if _, err := p.newMusicServiceControlPolicy(t.Context(), "start"); err == nil {
		t.Fatal("non-owner admitted")
	}
	// No privileged Agent trace is required for an authenticated local session.
	admin := &permittedTools{manager: f.manager}
	if _, err := admin.newMusicServiceControlPolicy(legacyLocalMusicAdminContext(t, f.manager), "status"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.newMusicServiceControlPolicy(t.Context(), "status"); err == nil {
		t.Fatal("unbound admin request admitted")
	}
}

func TestMusicServiceAuthenticatedRoutesUseInstalledCapability(t *testing.T) {
	f, _, store := musicServiceFixture(t)
	calls := 0
	c, err := pluginruntime.NewController(store, musicServiceFixtureExecutor{calls: &calls})
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageMu.Lock()
	f.manager.packageRuntime, f.manager.packageRuntimeReady = c, true
	f.manager.packageMu.Unlock()
	if code, _, _ := f.request("GET", "/api/speech/music-service", nil, true, false); code != 401 {
		t.Fatal("unauthenticated status admitted", code)
	}
	f.login()
	if code, body, _ := f.request("GET", "/api/speech/music-service", nil, true, false); code != 200 || calls != 1 || !strings.Contains(string(body), `"coverage":"owned_supervisor_only"`) {
		t.Fatal(code, string(body), calls)
	}
	if code, _, _ := f.request("POST", "/api/speech/music-service/stop", nil, true, false); code != 403 || calls != 1 {
		t.Fatal("CSRF-free stop admitted", code, calls)
	}
	if runtime.GOOS == "windows" {
		if code, body, _ := f.request("POST", "/api/speech/music-service/stop", nil, true, true); code != 200 || calls != 2 || !strings.Contains(string(body), `"state":"stopped"`) {
			t.Fatal(code, string(body), calls)
		}
		// The profile is deliberately absent: routing to the real host adapter
		// must fail without creating a real music supervisor.
		if code, _, _ := f.request("POST", "/api/speech/music-service/start", nil, true, true); code != 409 || calls != 3 {
			t.Fatal("missing service profile admitted", code, calls)
		}
	}
}
