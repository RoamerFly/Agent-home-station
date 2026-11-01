package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// musicPreparationForgeryExecutor is deliberately a test-only plugin process
// substitute. Controller, durable jobs, host policy checks and result parsing
// remain production code; only the package's pure reply is adversarial.
type musicPreparationForgeryExecutor struct{ reply json.RawMessage }

func (e musicPreparationForgeryExecutor) Start(_ context.Context, p pluginhost.Package, _ pluginhost.Grant) (pluginhost.Session, error) {
	if p.ID != "music" {
		return nil, errors.New("unexpected package")
	}
	return e, nil
}
func (musicPreparationForgeryExecutor) Health(ctx context.Context) error { return ctx.Err() }
func (musicPreparationForgeryExecutor) Close() error                     { return nil }
func (e musicPreparationForgeryExecutor) Call(_ context.Context, _ pluginhost.Invocation, tool string, _ json.RawMessage) (json.RawMessage, error) {
	if tool != musicPreparationCapability {
		return nil, errors.New("unexpected capability")
	}
	return append(json.RawMessage(nil), e.reply...), nil
}

// musicPreparationLiveController executes the checked-in package via the same
// isolated stdio runtime as production. Preparation is pure: this fixture has
// no HTTP server and must never request a local music bridge callback.
func musicPreparationLiveController(t *testing.T, store *pluginhost.Store) *pluginruntime.Controller {
	t.Helper()
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in official music preparation package contract")
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

func TestLiveMusicPreparationOfficialPackageBoundOutputs(t *testing.T) {
	f, access, store := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	controller := musicPreparationLiveController(t, store)
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	task := agentTask{ID: "music-preparation-job", Account: access.trace.Account, Thread: access.trace.Thread, Sender: access.trace.Sender, State: "waiting", MusicJob: "job-1", OriginTrace: access.trace.TraceID, Constraints: access.taskConstraints("music", "", "", "")}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	access.trace.TaskID = task.ID

	var style struct {
		Style string `json:"style"`
	}
	if err := access.prepareMusic(t.Context(), "/health", map[string]any{"action": "style", "style": "女声 民谣", "gender": "female", "mode": "cover"}, &style, func() error {
		if !strings.Contains(style.Style, "preserve original melody and lyrics") || !strings.HasSuffix(style.Style, "female vocal") {
			return context.Canceled
		}
		return nil
	}); err != nil {
		t.Fatal("official style preparation", err)
	}

	healthV11 := []byte(`{"ready":true,"bridge_version":11,"voice_profiles":false,"private_voice_profiles":false}`)
	if _, err := access.prepareRVCRequest(t.Context(), healthV11, "合成夹具", "registered-rvc", strings.Repeat("a", 64), 1.25, 8.5, []byte("synthetic-reference")); err == nil || !strings.Contains(err.Error(), "rvc_profile_bridge_required") {
		t.Fatal("v11 bridge accepted a selected voice profile", err)
	}
	healthV12 := []byte(`{"ready":true,"bridge_version":12,"voice_profiles":true,"private_voice_profiles":false}`)
	prepared, err := access.prepareRVCRequest(t.Context(), healthV12, "合成夹具", "registered-rvc", strings.Repeat("a", 64), 1.25, 8.5, []byte("synthetic-reference"))
	if err != nil {
		t.Fatal("official RVC preparation", err)
	}
	raw, _ := json.Marshal(prepared)
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body["voice_profile_id"] != "registered-rvc" || body["reference_source_sha256"] != strings.Repeat("a", 64) || body["reference_base64"] != base64.StdEncoding.EncodeToString([]byte("synthetic-reference")) || body["reference_pretrimmed"] != true {
		t.Fatal("RVC preparation changed selected profile/source/audio", string(raw))
	}

	status := map[string]any{"id": "job-1", "engine": "yue", "state": "failed"}
	var plan struct {
		Engine  string          `json:"engine"`
		State   string          `json:"state"`
		Submit  bool            `json:"submit"`
		Options map[string]bool `json:"options"`
	}
	if err = access.prepareMusic(t.Context(), "/jobs/job-1", map[string]any{"action": "resume_plan", "id": "job-1", "status": status, "restart_missing": true, "restart_failed": true}, &plan, func() error {
		if plan.Engine != "yue" || plan.State != "failed" || !plan.Submit || !plan.Options["restart_missing"] || !plan.Options["restart_failed"] {
			return context.Canceled
		}
		return nil
	}); err != nil {
		t.Fatal("official resume plan", err)
	}
	var accepted struct {
		Status map[string]any `json:"status"`
	}
	if err = access.prepareMusic(t.Context(), "/jobs/job-1", map[string]any{"action": "resume_result", "id": "job-1", "engine": "yue", "status": map[string]any{"id": "job-1", "engine": "yue", "state": "running"}}, &accepted, func() error {
		if accepted.Status["id"] != "job-1" || accepted.Status["engine"] != "yue" {
			return context.Canceled
		}
		return nil
	}); err != nil {
		t.Fatal("official resume result", err)
	}

	var recovered struct {
		ID     string         `json:"id"`
		Title  string         `json:"title"`
		Recipe map[string]any `json:"recipe"`
	}
	recipe := map[string]any{"title": "原曲", "mode": "cover", "lyrics": "合成歌词", "reference_sha256": strings.Repeat("b", 64), "reference_source_sha256": strings.Repeat("a", 64)}
	if err = access.prepareMusic(t.Context(), "/jobs/job-1", map[string]any{"action": "recover_recipe", "id": "job-1", "status": map[string]any{"id": "job-1", "state": "completed", "reference_used": true, "recipe": recipe}}, &recovered, func() error {
		if recovered.ID != "job-1" || recovered.Title != "原曲" || recovered.Recipe["reference_sha256"] != strings.Repeat("a", 64) {
			return context.Canceled
		}
		return nil
	}); err != nil {
		t.Fatal("official recovery recipe", err)
	}
	if err = access.prepareMusic(t.Context(), "/jobs/job-1", map[string]any{"action": "resume_result", "id": "job-1", "engine": "yue", "status": map[string]any{"id": "other", "engine": "yue", "state": "running"}}, &accepted, nil); err == nil || !strings.Contains(err.Error(), "music_resume_identity_invalid") {
		t.Fatal("mismatched recovered identity accepted", err)
	}
	if f.native.loginCalls.Load() != 0 {
		t.Fatal("pure preparation touched native WeChat")
	}
}

func TestMusicPreparationPolicyRejectsReplacedOutputAndDisabledPackage(t *testing.T) {
	f, access, store := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	base, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/health", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"action":"style","style":"民谣","gender":"female","mode":"original"}`)
	var output struct {
		Style string `json:"style"`
	}
	policy := &musicPreparationPolicy{base: base, arguments: args, output: &output, verify: func() error {
		if output.Style != "民谣, female vocal" {
			return context.Canceled
		}
		return nil
	}}
	installed, err := store.Installed(t.Context(), "music")
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.CreateJob(t.Context(), pluginhost.Job{Plugin: "music", Tool: musicPreparationCapability, Version: installed.Package.Version, PackageDigest: installed.Digest, ConfigRevision: "1", Context: base.invocation, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.ClaimJob(t.Context(), job.ID, job.Revision)
	if err != nil {
		t.Fatal(err)
	}
	cap := pluginhost.Capability{ID: musicPreparationCapability, Implementation: "ready", Permissions: []string{"network"}}
	if _, err = policy.Validate(t.Context(), job, cap, json.RawMessage(`{"style":"forged, female vocal"}`)); err == nil {
		t.Fatal("replaced pure-plugin output accepted")
	}
	if _, err = store.SetEnabled(t.Context(), "music", installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	if err = policy.Check(t.Context(), job, cap); err == nil {
		t.Fatal("disabled package retained a preparation job authorization")
	}
	if f.native.loginCalls.Load() != 0 {
		t.Fatal("policy check touched native WeChat")
	}
}

func TestPrepareRVCRequestRejectsForgedPluginBoundFieldsBeforeHTTP(t *testing.T) {
	for _, forge := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing_profile", func(body map[string]any) { delete(body, "voice_profile_id") }},
		{"default_profile", func(body map[string]any) { body["voice_profile_id"] = "default-rvc" }},
		{"source_hash", func(body map[string]any) { body["reference_source_sha256"] = strings.Repeat("c", 64) }},
		{"audio_ref", func(body map[string]any) {
			body["reference_base64"] = map[string]any{"$music_ref": map[string]any{"ref": "reference_0", "sha256": strings.Repeat("d", 64), "bytes": 1}}
		}},
	} {
		t.Run(forge.name, func(t *testing.T) {
			f, access, store := localMusicPluginFixture(t, "http://127.0.0.1:8765")
			clip := []byte("synthetic-reference")
			body := map[string]any{
				"title": "合成夹具", "reference_base64": map[string]any{"$music_ref": map[string]any{"ref": "reference_0", "sha256": namedSpeechSHA(clip), "bytes": len(clip)}},
				"reference_source_sha256": strings.Repeat("a", 64), "reference_start_seconds": 1.25, "reference_seconds": 8.5, "reference_pretrimmed": true, "voice_profile_id": "registered-rvc",
			}
			forge.mutate(body)
			raw, _ := json.Marshal(map[string]any{"method": "POST", "path": "/rvc-jobs", "body": body})
			controller, err := pluginruntime.NewController(store, musicPreparationForgeryExecutor{reply: raw})
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
			health := []byte(`{"ready":true,"bridge_version":12,"voice_profiles":true,"private_voice_profiles":false}`)
			if _, err = access.prepareRVCRequest(t.Context(), health, "合成夹具", "registered-rvc", strings.Repeat("a", 64), 1.25, 8.5, clip); err == nil {
				t.Fatal("forged RVC preparation reached a submit-capable result")
			}
			if f.native.loginCalls.Load() != 0 {
				t.Fatal("forged preparation touched native WeChat")
			}
		})
	}
}
