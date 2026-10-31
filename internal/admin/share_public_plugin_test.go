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
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func publicPolicyFixture(t *testing.T, remember bool, kind int) (*webFixture, *publicSharePolicy, pluginhost.Job, context.Context) {
	t.Helper()
	f, n, _, ctx := receivedPolicyFixtureFor(t, remember, kind)
	p, err := f.manager.newPublicSharePolicy(ctx, n.msg)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"url": n.msg.Link.URL, "video_required": kind == 4})
	return f, p, pluginhost.Job{Plugin: "share-reader", Tool: "share.resolve_public", Context: p.invocation(), Arguments: args}, ctx
}

func publicObservation() publicShareObservation {
	no := false
	return publicShareObservation{OK: true, Platform: "public", SourceURL: "https://public.example/share", SHA: strings.Repeat("a", 64), RequestedLimit: 14000, RequestedCandidateLimit: 20, TotalCandidates: 1, Locators: []string{"https://cdn.example/video.mp4?signature=synthetic"}, Coverage: "media_locator_only", Observed: &no, Downloaded: &no, Untrusted: true, Complete: true}
}

func publicPolicyRead(t *testing.T, p *publicSharePolicy, j pluginhost.Job, ctx context.Context) {
	t.Helper()
	p.read = func(context.Context, string, int64, time.Duration) ([]byte, string, string, error) {
		return []byte(`<video src="https://cdn.example/video.mp4?signature=synthetic"></video>`), "text/html", p.message.Link.URL, nil
	}
	args, _ := json.Marshal(map[string]any{"url": p.message.Link.URL, "max_bytes": 1024, "timeout_seconds": 2})
	if _, err := p.Service(ctx, j, pluginhost.Capability{}, "wechat/http_read", args); err != nil {
		t.Fatal(err)
	}
}

func TestPublicShareProofAndPrivateProjection(t *testing.T) {
	_, p, j, ctx := publicPolicyFixture(t, false, 4)
	raw, _ := json.Marshal(publicObservation())
	if _, err := p.Validate(ctx, j, pluginhost.Capability{}, raw); err == nil {
		t.Fatal("unread source accepted")
	}
	publicPolicyRead(t, p, j, ctx)
	verified, err := p.Validate(ctx, j, pluginhost.Capability{}, raw)
	if err != nil || len(verified.Private) == 0 || strings.Contains(string(verified.Result), "signature") {
		t.Fatal("unsafe projection", err)
	}
	for _, name := range []string{"locator", "source", "observed", "pagination", "arguments", "duplicate", "unknown"} {
		t.Run(name, func(t *testing.T) {
			r := publicObservation()
			switch name {
			case "locator":
				r.Locators = []string{"https://foreign.example/private.mp4"}
			case "source":
				r.SourceURL = "https://other.example/share"
			case "observed":
				yes := true
				r.Observed = &yes
			case "pagination":
				r.TotalCandidates = 2
			case "arguments":
				r.RequestedLimit = 1
			}
			b, _ := json.Marshal(r)
			if name == "duplicate" {
				b = append([]byte(`{"ok":true,`), b[1:]...)
			}
			if name == "unknown" {
				b = append([]byte(`{"send_to":"another-room",`), b[1:]...)
			}
			if _, e := p.Validate(ctx, j, pluginhost.Capability{}, b); e == nil {
				t.Fatal("unverified result accepted")
			}
		})
	}
	// Explicit page sizes are honored, rather than clamped to automatic defaults.
	j.Arguments = json.RawMessage(`{"url":"https://public.example/share?key=synthetic","video_required":true,"limit":7,"candidate_limit":3}`)
	r := publicObservation()
	r.RequestedLimit = 7
	r.RequestedCandidateLimit = 3
	b, _ := json.Marshal(r)
	if _, err = p.Validate(ctx, j, pluginhost.Capability{}, b); err != nil {
		t.Fatal(err)
	}
}

func TestPublicShareReadAndDownloadRecheckAuthorization(t *testing.T) {
	for _, remember := range []bool{false, true} {
		t.Run(map[bool]string{false: "ephemeral", true: "remembered"}[remember], func(t *testing.T) {
			f, p, j, ctx := publicPolicyFixture(t, remember, 4)
			publicPolicyRead(t, p, j, ctx)
			r := publicObservation()
			p.media = func(context.Context, string, string, int64) ([]byte, string, error) {
				f.manager.mcp.WebFetchEnabled = false
				return []byte{0, 0, 0, 24, 'f', 't', 'y', 'p'}, "video/mp4", nil
			}
			if _, err := p.handoff(ctx, r, j, 1024); err == nil {
				t.Fatal("download completed after revocation")
			}
			f.manager.mcp.WebFetchEnabled = true
			foreign := j
			foreign.Context.Thread = "another-room"
			if err := p.Check(ctx, foreign, pluginhost.Capability{}); err == nil {
				t.Fatal("foreign invocation accepted")
			}
			if remember {
				if err := f.manager.memory.Clear(companion.ScopeID("wechat", "test", "filehelper")); err != nil {
					t.Fatal(err)
				}
				if _, err := p.handoff(ctx, r, j, 1024); err == nil {
					t.Fatal("forgotten source delivered")
				}
			} else if _, err := f.manager.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", "test", "filehelper")}, p.source.EventID); err == nil {
				t.Fatal("no-memory source persisted")
			}
		})
	}
}

func TestPublicShareReadBoundsAndActualMediaFailure(t *testing.T) {
	f, p, j, ctx := publicPolicyFixture(t, false, 4)
	for _, raw := range []string{
		`{"url":"https://other.example/","max_bytes":100,"timeout_seconds":1}`,
		`{"url":"https://public.example/share?key=synthetic","max_bytes":100,"max_bytes":200,"timeout_seconds":1}`,
		`{"url":"https://public.example/share?key=synthetic","max_bytes":99999999,"timeout_seconds":1}`,
	} {
		if _, err := p.Service(ctx, j, pluginhost.Capability{}, "wechat/http_read", json.RawMessage(raw)); err == nil {
			t.Fatal("bad read accepted")
		}
	}
	publicPolicyRead(t, p, j, ctx)
	r := publicObservation()
	p.media = func(context.Context, string, string, int64) ([]byte, string, error) {
		return []byte("<html>sign in</html>"), "video/mp4", nil
	}
	if _, err := p.handoff(ctx, r, j, 1024); err == nil {
		t.Fatal("login HTML accepted as video")
	}
	p.read = func(context.Context, string, int64, time.Duration) ([]byte, string, string, error) {
		f.manager.mcp.WebFetchEnabled = false
		return []byte("page"), "text/html", p.message.Link.URL, nil
	}
	raw, _ := json.Marshal(map[string]any{"url": p.message.Link.URL, "max_bytes": 1024, "timeout_seconds": 2})
	if _, err := p.Service(ctx, j, pluginhost.Capability{}, "wechat/http_read", raw); err == nil {
		t.Fatal("read accepted after revocation")
	}
}

func TestPublicShareLocatorEvidenceEscaping(t *testing.T) {
	got := publicShareLocations([]byte(`<video src="/media.mp4?a=1&amp;b=2"></video><script>{"url":"https:\/\/cdn.example\/video.mp4?a=1\u0026b=2"}</script>`), "https://page.example/work")
	for _, s := range []string{"https://page.example/media.mp4?a=1&b=2", "https://cdn.example/video.mp4?a=1&b=2"} {
		if !got[s] {
			t.Fatal("lost escaped locator", s)
		}
	}
}

func TestPublicShareDisabledNeverFallsBack(t *testing.T) {
	f, p, _, ctx := publicPolicyFixture(t, false, 4)
	i, err := p.store.Installed(ctx, "share-reader")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.store.SetEnabled(ctx, i.Package.ID, i.Revision, false); err != nil {
		t.Fatal(err)
	}
	_, err = (mediaUnderstanding{manager: f.manager}).understandSharedVideo(ctx, p.message)
	if err == nil || err.Error() != "share_plugin_disabled" {
		t.Fatal("disabled plugin reached legacy network path", err)
	}
}

func TestLivePublicShareBodyAndFailures(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated Python; synthetic reads only")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	for _, tc := range []struct {
		name, kind, body, failure string
		subtype                   int
	}{
		{"text", "text/html", "<article>" + strings.Repeat("合成正文。", 40) + "</article>", "", 5},
		{"direct", "video/mp4", "", "", 4},
		{"landing", "text/html", "<article>" + strings.Repeat("应用广告。", 40) + "</article>", "share_requires_browser", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, p, _, ctx := publicPolicyFixture(t, false, tc.subtype)
			controller, err := pluginruntime.NewController(p.store, pluginruntime.InstalledExecutor(p.store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
			reads := 0
			p.read = func(context.Context, string, int64, time.Duration) ([]byte, string, string, error) {
				reads++
				return []byte(tc.body), tc.kind, p.message.Link.URL, nil
			}
			result, job, err := p.resolve(ctx)
			if tc.failure != "" {
				if err == nil || err.Error() != tc.failure || job.State != "failed" {
					t.Fatal("lost actual failure", job.State, err)
				}
				_, _, again := p.resolve(ctx)
				if again == nil || again.Error() != tc.failure || reads != 1 {
					t.Fatal("failed parser resubmitted", reads, again)
				}
				return
			}
			if err != nil || job.State != "completed" {
				t.Fatal(job.State, err)
			}
			if tc.name == "text" {
				page, err := p.handoff(ctx, result, job, 1024)
				if err != nil || page.Kind != "page" || !strings.Contains(page.Text, "合成正文") {
					t.Fatal("lost actual text", err)
				}
			} else if len(result.Locators) != 1 || result.Locators[0] != p.message.Link.URL || *result.Downloaded || *result.Observed {
				t.Fatal("direct media incorrectly marked observed")
			}
		})
	}
}

func TestLivePublicSharePolicyIsolatedExecutionAndRecovery(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated Python; synthetic reads only")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	for _, remember := range []bool{false, true} {
		t.Run(map[bool]string{false: "ephemeral", true: "remembered"}[remember], func(t *testing.T) {
			f, p, _, ctx := publicPolicyFixture(t, remember, 4)
			controller, err := pluginruntime.NewController(p.store, pluginruntime.InstalledExecutor(p.store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
			reads := 0
			p.read = func(context.Context, string, int64, time.Duration) ([]byte, string, string, error) {
				reads++
				return []byte(`<title>Synthetic</title><video src="https://cdn.example/video.mp4?signature=synthetic"></video>`), "text/html", p.message.Link.URL, nil
			}
			result, job, err := p.resolve(ctx)
			if err != nil || result.Coverage != "media_locator_only" || reads != 1 {
				t.Fatal("isolated resolution", job.State, reads, err)
			}
			if strings.Contains(string(job.Result), "signature") {
				t.Fatal("locator leaked")
			}
			// A fresh policy after process restart must restore the checked private
			// handoff, not rerun the parser or trust an unverified public projection.
			if err = controller.Close(); err != nil {
				t.Fatal(err)
			}
			controller, err = pluginruntime.NewController(p.store, pluginruntime.InstalledExecutor(p.store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			f.manager.packageRuntime = controller
			fresh, err := f.manager.newPublicSharePolicy(ctx, p.message)
			if err != nil {
				t.Fatal(err)
			}
			fresh.read = func(context.Context, string, int64, time.Duration) ([]byte, string, string, error) {
				return nil, "", "", errors.New("must reuse checkpoint")
			}
			again, reused, err := fresh.resolve(ctx)
			if err != nil || reused.ID != job.ID || again.SHA != result.SHA {
				t.Fatal("checkpoint recovery", err)
			}
			downloaded := 0
			fresh.media = func(_ context.Context, locator, source string, limit int64) ([]byte, string, error) {
				downloaded++
				if locator != result.Locators[0] || source != result.SourceURL || limit != 1024 {
					t.Fatal("handoff changed")
				}
				return []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0}, "video/mp4", nil
			}
			page, err := fresh.handoff(ctx, again, reused, 1024)
			if err != nil || page.Kind != "video" || downloaded != 1 {
				t.Fatal("controlled handoff", err)
			}
			fresh.media = func(context.Context, string, string, int64) ([]byte, string, error) {
				i, e := p.store.Installed(ctx, "share-reader")
				if e != nil {
					t.Fatal(e)
				}
				if e = controller.Stop(ctx, i.Package.ID, i.Revision); e != nil {
					t.Fatal(e)
				}
				cfg, e := p.store.Configuration(ctx, i.Package.ID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = p.store.SaveConfiguration(ctx, i.Package.ID, pluginhost.ConfigUpdate{Revision: cfg.Revision, Values: cfg.Values}); e != nil {
					t.Fatal(e)
				}
				i, e = p.store.Installed(ctx, i.Package.ID)
				if e != nil {
					t.Fatal(e)
				}
				if e = controller.Start(ctx, i.Package.ID, i.Revision); e != nil {
					t.Fatal(e)
				}
				return page.Data, page.ContentType, nil
			}
			if _, err = fresh.handoff(ctx, again, reused, 1024); err == nil || err.Error() != "share_plugin_snapshot_changed" {
				t.Fatal("configuration change did not reject the old snapshot as expected", err)
			}
			// This checks the transfer/type contract, not video decoding or understanding.
		})
	}
}
