package admin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// namedSpeechLiveController starts the official speech package in the same
// isolated stdio host used in deployment.  The only synthetic part is p.run:
// it produces a deterministic WAV in the host-owned broker, so this test never
// imports Torch, requests a model, or sends a WeChat message.
func namedSpeechLiveController(t *testing.T, store *pluginhost.Store) *pluginruntime.Controller {
	t.Helper()
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in LPAC named-speech package contract")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	options := pluginruntime.Options{Python: python, RuntimeRoots: []string{filepath.Dir(python)}}
	if runtime.GOOS == "windows" {
		options.Pythonw = filepath.Join(filepath.Dir(python), "pythonw.exe")
		options.WindowsHelper = helper
	}
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, options))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	return controller
}

func TestLiveNamedSpeechOfficialPackageProducesBoundWAVAndPrivateCheckpoint(t *testing.T) {
	f, _, policy, _, _ := namedSpeechContractFixture(t)
	controller := namedSpeechLiveController(t, policy.store)
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	calls := 0
	policy.run = namedSpeechSyntheticRunner(t, policy, &calls)

	result, err := policy.synthesize(t.Context())
	if err != nil || calls != 1 || result.Status != "synthesized" || result.Audio.Ref != "audio_0" || result.Audio.Bytes != int64(len(syntheticNamedSpeechWAV())) || policy.completedJob.State != "completed" {
		page, _ := policy.store.Jobs(t.Context(), "speech", 0, 8)
		t.Fatal("official named-speech package did not produce one verified WAV", result, err, calls, policy.completedJob, controller.Status("speech"), page)
	}
	data, err := policy.readAudio(t.Context())
	if err != nil || !bytes.Equal(data, syntheticNamedSpeechWAV()) {
		t.Fatal("official named-speech output was not readable through the guarded checkpoint", err)
	}
	private, err := policy.store.PrivateJobResult(t.Context(), policy.completedJob, func(ctx context.Context, job pluginhost.Job) error {
		return policy.Check(ctx, job, pluginhost.Capability{})
	})
	if err != nil || !bytes.Contains(private, []byte(`"binding"`)) || !bytes.Contains(private, []byte(result.Audio.SHA256)) {
		t.Fatal("named-speech private checkpoint missing", err, string(private))
	}
	page, err := policy.store.Jobs(t.Context(), "speech", 0, 8)
	if err != nil || len(page.Items) != 1 || bytes.Contains(page.Items[0].Result, []byte("speech.wav")) || bytes.Contains(page.Items[0].Result, []byte(policy.checkpoint.Directory)) {
		t.Fatal("named-speech private execution data leaked into the public job", err, page)
	}
	if f.native.loginCalls.Load() != 0 {
		t.Fatal("named-speech contract touched the native WeChat transport")
	}
}

func TestLiveNamedSpeechRevocationAndCancellationStopCompletionWithoutReplay(t *testing.T) {
	for _, mode := range []string{"revoked", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, _, policy, _, _ := namedSpeechContractFixture(t)
			controller := namedSpeechLiveController(t, policy.store)
			f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
			calls := 0
			policy.run = namedSpeechSyntheticRunner(t, policy, &calls)
			baseRun := policy.run
			policy.run = func(ctx context.Context, cfg agenthost.Config, cwd string, argv []string) ([]byte, []byte, error) {
				calls++
				if mode == "revoked" {
					f.manager.config.Companion.Permissions.SendVoice = false
				} else {
					page, err := policy.store.Jobs(ctx, "speech", 0, 8)
					if err != nil || len(page.Items) != 1 {
						t.Fatal("running named-speech job not found for cancellation", err, page)
					}
					if _, err = policy.store.RequestJobCancel(ctx, page.Items[0].ID, page.Items[0].Revision); err != nil {
						t.Fatal("failed to cancel running named-speech job", err)
					}
				}
				// baseRun increments itself.  Decrement this wrapper accounting so
				// assertions continue to count the one actual broker attempt.
				calls--
				return baseRun(ctx, cfg, cwd, argv)
			}
			_, err := policy.synthesize(t.Context())
			if err == nil || calls != 1 {
				t.Fatal("revoked or cancelled named speech unexpectedly completed/replayed", mode, err, calls)
			}
			page, pageErr := policy.store.Jobs(t.Context(), "speech", 0, 8)
			if pageErr != nil || len(page.Items) != 1 || page.Items[0].State == "completed" {
				t.Fatal("revoked or cancelled named speech recorded a completion", mode, pageErr, page)
			}
			if _, readErr := policy.readAudio(t.Context()); readErr == nil {
				t.Fatal("revoked or cancelled named speech exposed output audio", mode)
			}
			if f.native.loginCalls.Load() != 0 {
				t.Fatal("named speech revocation path touched native WeChat transport")
			}
		})
	}
}
