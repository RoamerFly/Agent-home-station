package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// The speech result was already produced by the synthetic runner. Its session
// can be opened, but the music-only executor will fail if inference is replayed.
type speechReplayMusicExecutor struct{ legacyLocalMusicExecutor }

func (e speechReplayMusicExecutor) Start(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginhost.Session, error) {
	if p.ID == "speech" {
		return e.legacyLocalMusicExecutor, nil
	}
	return e.legacyLocalMusicExecutor.Start(ctx, p, g)
}

func TestSpokenRVCRetryUsesOnlyVerifiedDraftAndDoesNotSubmitTwice(t *testing.T) {
	f, p, original, _, cap := namedSpeechContractFixture(t)
	p = p.ForkTools().(*permittedTools)
	trace := original.p.trace
	trace.Text = "用测试角色的声线读这段话"
	p.SetTrace(trace)
	target := voiceEntry{ID: "synthetic-rvc", Name: "卡通-测试角色", Role: "测试角色", Engine: "RVC", ProfileID: "voice-test"}
	dir := filepath.Join(p.executionContext().Public, "models", "voice-library")
	if err := ensureManaged(dir); err != nil {
		t.Fatal(err)
	}
	cat, _ := json.Marshal(map[string]any{"items": []voiceEntry{target}})
	if err := writePrivate(filepath.Join(dir, "catalog.json"), cat); err != nil {
		t.Fatal(err)
	}
	var posts int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rvc-health":
			w.Write([]byte(`{"ready":true,"bridge_version":12,"voice_profiles":true}`))
		case "/rvc-jobs":
			posts++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["voice_profile_id"] != target.ProfileID || body["reference_source_sha256"] != namedSpeechSHA(syntheticReferenceWAV(t, 2)) {
				t.Error("wrong profile or draft", err)
			}
			w.Write([]byte(`{"id":"spoken-rvc-job"}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	cfg := readLegacySpeechTestConfig(t, f.manager)
	cfg.YuEEnabled, cfg.YuEEndpoint, cfg.YuEToken = true, upstream.URL, "synthetic"
	writeLegacySpeechMigrationFixture(t, f.manager, cfg)
	exec := installLegacyLocalMusicPlugin(t, f.manager)
	c, err := pluginruntime.NewController(original.store, speechReplayMusicExecutor{exec})
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime = c
	t.Cleanup(func() { _ = c.Close() })
	if err := validateRequestedSpeechVoice(trace.Text, original.resource.Voice.ID, []voiceEntry{target, original.resource.Voice}); err == nil {
		t.Fatal("role alias allowed an unrelated final voice")
	}
	args := json.RawMessage(`{"text":"合成验收","voice_id":"synthetic-named-voice","rvc_target_id":"synthetic-rvc"}`)
	base, _, err := splitRVCTarget(args)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.bindSelectedVoice(target.ID, target.ProfileID); err != nil {
		t.Fatal(err)
	}
	policy, err := p.namedSpeechPolicy(t.Context(), base, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	completeNamedSpeechCheckpoint(t, policy, cap)
	if _, err = policy.stage(t.Context()); err == nil {
		t.Fatal("staged the intermediate draft")
	}
	p = p.ForkTools().(*permittedTools)
	p.SetTrace(trace)
	for i := 0; i < 2; i++ {
		result, err := p.Call(t.Context(), "speech_synthesize", args)
		if err != nil || !strings.Contains(result, "spoken-rvc-job") || strings.Contains(result, `"media_ids"`) {
			t.Fatal("conversion failed", result, err)
		}
	}
	tasks := f.manager.agent.list(trace.Account, trace.Thread)
	if posts != 1 || len(tasks) != 1 || tasks[0].Constraints.VoiceID != target.ID || tasks[0].MusicSpec.VoiceProfile != target.ProfileID || !strings.HasPrefix(tasks[0].MusicSpec.Reference, "speech-job-") || len(p.media) != 0 {
		t.Fatal("unexpected delivery", posts, tasks, len(p.media))
	}
}
