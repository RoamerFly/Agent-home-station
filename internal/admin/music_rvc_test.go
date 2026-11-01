package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestDirectRVCCoverUsesWholeActualRecordingWithoutYuEOrASRLyrics(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "rvc-request", Mode: "direct", Text: "翻唱这首"}
	p.SetTrace(in)
	asset, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: syntheticReferenceWAV(t, 72), MIME: "audio/wav", Filename: "synthetic-original.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	posts := 0
	var submitted map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rvc-health":
			w.Write([]byte(`{"ready":true,"bridge_version":9}`))
		case "/rvc-jobs":
			posts++
			json.NewDecoder(r.Body).Decode(&submitted)
			w.Write([]byte(`{"id":"synthetic-rvc-job","engine":"rvc","state":"queued"}`))
		case "/jobs/synthetic-rvc-job":
			w.Write([]byte(`{"id":"synthetic-rvc-job","engine":"rvc","state":"running"}`))
		default:
			t.Error("RVC called a YuE generation endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer upstream.Close()
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, upstream.URL, "synthetic-token"
	installLegacyLocalMusicPlugin(t, f.manager)
	p.settings.Enabled = false // A local music tool is independent of web/MCP switches.
	raw, _ := json.Marshal(map[string]any{"title": "原曲换声", "reference_asset_id": asset.ID})
	for i := 0; i < 2; i++ {
		result, err := p.Call(context.Background(), "music_rvc_start", raw)
		if err != nil || !strings.Contains(result, `"automatic_followup":true`) {
			t.Fatal(result, err)
		}
	}
	if posts != 1 || submitted["reference_seconds"] != float64(72) || submitted["reference_pretrimmed"] != true || submitted["reference_source_sha256"] != asset.Hash {
		t.Fatal("source length/identity lost or conversion replayed", posts, submitted)
	}
	if _, ok := submitted["lyrics"]; ok {
		t.Fatal("RVC used rewritten lyrics")
	}
	if _, ok := submitted["style"]; ok {
		t.Fatal("RVC used a composition prompt")
	}
	tasks := f.manager.agent.list("test", "filehelper")
	if len(tasks) != 1 || tasks[0].MusicSpec.Engine != "rvc" || tasks[0].MusicSpec.ReferenceSeconds != 72 {
		t.Fatal(tasks)
	}
	status, err := p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"synthetic-rvc-job"}`))
	if err != nil || !strings.Contains(status, `"running"`) {
		t.Fatal("RVC job not readable by normal continuation", status, err)
	}
	p.trace.Thread = "other-room"
	if _, err = p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"synthetic-rvc-job"}`)); err == nil {
		t.Fatal("cross-room RVC job leaked")
	}
	if _, err = p.callYuE(context.Background(), "music_rvc_start", raw); err == nil {
		t.Fatal("cross-room source readable")
	}
}

func TestNamedRVCVoiceCannotFallBackWhenRememberedNameIsOnlyInTitle(t *testing.T) {
	voices := []voiceEntry{{ID: "private-speech", Name: "Aurora", Engine: "GPT-SoVITS"}, {ID: "private-singing", Name: "Aurora", Engine: "RVC", ProfileID: "private-aurora"}, {ID: "other", Name: "Other", Engine: "RVC", ProfileID: "other-profile"}}
	for _, text := range []string{"请用 Aurora 声线唱", "Aurora翻唱", "aurora cover"} {
		if e := validateRequestedRVCVoice(text, "", "", voices); e == nil {
			t.Fatal("named voice silently used the default", text)
		}
		if e := validateRequestedRVCVoice(text, "other", "other-profile", voices); e == nil {
			t.Fatal("wrong registered voice accepted", text)
		}
		if e := validateRequestedRVCVoice(text, "private-singing", "private-aurora", voices); e != nil {
			t.Fatal(e)
		}
	}
	if e := validateRequestedRVCVoice("翻唱这首歌，任意声线", "", "", voices); e != nil {
		t.Fatal("unspecified voice default changed", e)
	}
}

func TestPrivateRVCProfileRequiresNewBridgeAndVerifiedOwner(t *testing.T) {
	f, p, private, _ := voiceAgentFixture(t)
	private.Engine, private.ProfileID = "RVC", "private-synthetic-singer"
	data, _ := json.Marshal(map[string]any{"items": []voiceEntry{private}})
	if e := os.WriteFile(filepath.Join(f.manager.privateVoiceLibrary(), "catalog.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "private-singing", Text: "请用 owner-voice 翻唱"}
	p.SetTrace(in)
	asset, e := f.manager.rememberAssetFormats(in, assetOriginal{Data: syntheticReferenceWAV(t, 2), MIME: "audio/wav", Filename: "synthetic.wav"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	capable, posts := false, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rvc-health" {
			json.NewEncoder(w).Encode(map[string]any{"ready": true, "bridge_version": 13, "voice_profiles": true, "private_voice_profiles": capable})
			return
		}
		if r.URL.Path != "/rvc-jobs" {
			t.Error(r.URL.Path)
			w.WriteHeader(500)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["voice_profile_id"] != private.ProfileID {
			t.Error("private model replaced by default", body)
		}
		posts++
		w.Write([]byte(`{"id":"private-rvc-job"}`))
	}))
	defer upstream.Close()
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, upstream.URL, "synthetic"
	installLegacyLocalMusicPlugin(t, f.manager)
	raw, _ := json.Marshal(map[string]any{"title": "synthetic", "reference_asset_id": asset.ID, "voice_id": private.ID})
	if _, e = p.Call(t.Context(), "music_rvc_start", raw); e == nil || posts != 0 {
		t.Fatal("unsupported private bridge silently fell back", e, posts)
	}
	capable = true
	if _, e = p.Call(t.Context(), "music_rvc_start", raw); e != nil || posts != 1 {
		t.Fatal(e, posts)
	}
	p.trace.Sender = "member"
	if _, e = p.Call(t.Context(), "music_rvc_start", raw); e == nil || posts != 1 {
		t.Fatal("private singing model accessible after owner revocation", e, posts)
	}
}

func TestDirectRVCSelectedVoiceRequiresCapableBridgeAndSurvivesFollowup(t *testing.T) {
	_, p, _, public := voiceAgentFixture(t)
	public.ProfileID = "voice-manbo"
	public.Engine = "RVC"
	catalog, _ := json.Marshal(map[string]any{"items": []voiceEntry{public}})
	if err := os.WriteFile(filepath.Join(p.executionContext().Public, "models", "voice-library", "catalog.json"), catalog, 0600); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "selected-rvc", Mode: "direct"}
	p.SetTrace(in)
	asset, err := p.manager.rememberAssetFormats(in, assetOriginal{Data: syntheticReferenceWAV(t, 2), MIME: "audio/wav", Filename: "synthetic.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	version, posts := 11, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rvc-health" {
			json.NewEncoder(w).Encode(map[string]any{"ready": true, "bridge_version": version, "voice_profiles": version >= 12})
			return
		}
		if r.URL.Path != "/rvc-jobs" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		posts++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["voice_profile_id"] != "voice-manbo" {
			t.Error("selected profile lost", body)
		}
		w.Write([]byte(`{"id":"selected-rvc-job","voice_profile_id":"voice-manbo"}`))
	}))
	defer upstream.Close()
	p.manager.speech.YuEEnabled, p.manager.speech.YuEEndpoint, p.manager.speech.YuEToken = true, upstream.URL, "synthetic"
	installLegacyLocalMusicPlugin(t, p.manager)
	raw, _ := json.Marshal(map[string]any{"title": "synthetic", "reference_asset_id": asset.ID, "voice_profile_id": "voice-manbo"})
	if _, err = p.Call(context.Background(), "music_rvc_start", raw); err == nil || posts != 0 {
		t.Fatal("old bridge accepted selected profile", err, posts)
	}
	version = 12
	if _, err = p.Call(context.Background(), "music_rvc_start", raw); err != nil {
		t.Fatal(err)
	}
	tasks := p.manager.agent.list("test", "filehelper")
	if posts != 1 || len(tasks) != 1 || tasks[0].MusicSpec.VoiceProfile != "voice-manbo" {
		t.Fatal("followup lost voice identity", posts, tasks)
	}
}
