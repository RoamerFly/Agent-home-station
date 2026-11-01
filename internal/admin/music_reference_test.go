package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"wechat-bot/internal/companion"
)

func TestYuECoverPassesExactReferenceLyricsAndZeroSeedAndRetainsRecipe(t *testing.T) {
	f := setup(t)
	f.manager.config.Companion.Permissions.Remember = true
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "cover-request"}
	p.SetTrace(in)
	ref, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: syntheticReferenceWAV(t, 120), MIME: "audio/wav", Filename: "first-original.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lyrics := "[Verse]\n用户提供的原词\n[Chorus]\n不要改成新歌词"
	evidence, err := p.saveLyricEvidence(ref, lyrics, "user_supplied", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var submitted map[string]any
	var posts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":7}`))
			return
		}
		posts.Add(1)
		json.NewDecoder(r.Body).Decode(&submitted)
		w.Write([]byte(`{"id":"cover-job","state":"queued","seed":0,"reference_used":true}`))
	}))
	defer upstream.Close()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEnabled, legacySpeechFixture.YuEEndpoint, legacySpeechFixture.YuEToken = true, upstream.URL, "synthetic-token"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	installLegacyLocalMusicPlugin(t, f.manager)
	raw, _ := json.Marshal(map[string]any{"title": "原曲女声版", "mode": "cover", "style": "female folk", "lyrics": lyrics, "lyrics_evidence_ids": []string{evidence.ID}, "seed": 0, "reference_mode": "full", "duration_seconds": 60, "reference_asset_id": ref.ID, "text_guidance_scale": 2.7, "min_duration_seconds": 55})
	if _, err = p.callYuE(context.Background(), "music_local_start", raw); err != nil {
		t.Fatal(err)
	}
	if submitted["seed"] != float64(0) || submitted["mode"] != "cover" || submitted["reference_mode"] != "full" || !strings.Contains(submitted["lyrics"].(string), "用户提供的原词") {
		t.Fatal("parameters lost", submitted)
	}
	if submitted["text_guidance_scale"] != 2.7 || submitted["min_duration_seconds"] != float64(55) {
		t.Fatal("native controls were lost", submitted)
	}
	decoded, err := base64.StdEncoding.DecodeString(submitted["reference_base64"].(string))
	if err != nil || len(decoded) < 8 || string(decoded[4:8]) != "ftyp" || submitted["reference_pretrimmed"] != true || submitted["reference_source_sha256"] != ref.Hash {
		t.Fatal("wrong reference")
	}
	if _, err = p.callYuE(context.Background(), "music_local_start", raw); err != nil || posts.Load() != 1 {
		t.Fatal("cover resubmitted", err, posts.Load())
	}
	recovered, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks := recovered.list("test", "filehelper")
	if len(tasks) != 1 || tasks[0].MusicSpec == nil || tasks[0].MusicSpec.Reference != ref.ID || tasks[0].MusicSpec.Seed != 0 {
		t.Fatal("recipe did not survive restart", tasks)
	}
	if tasks[0].MusicSpec.TextGuidance != 2.7 || tasks[0].MusicSpec.MinimumDuration != 55 {
		t.Fatal("quality controls did not survive restart")
	}
}
func TestYuECoverMissingReferenceAndCrossScopeFailBeforeSubmission(t *testing.T) {
	f := setup(t)
	f.manager.config.Companion.Permissions.Remember = true
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "test"})
	for _, body := range []string{`{"title":"测试翻唱","style":"folk","lyrics":"supplied","mode":"cover"}`, `{"title":"测试翻唱","style":"folk","lyrics":"supplied","mode":"cover","reference_asset_id":"asset-from-other-room"}`, `{"title":"测试歌曲","style":"folk","lyrics":"supplied","mode":"original","seed":-1}`} {
		if _, err := p.callYuE(context.Background(), "music_local_start", json.RawMessage(body)); err == nil {
			t.Fatal("invalid cover accepted")
		}
	}
	if len(f.manager.agent.data.Operations) != 0 {
		t.Fatal("failed validation claimed generation")
	}
}
func TestYuEStableUnspecifiedSeedSurvivesFormattingAndNewRequestRerolls(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "first"})
	seeds := []float64{}
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":7}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		seeds = append(seeds, body["seed"].(float64))
		json.NewEncoder(w).Encode(map[string]string{"id": "job-" + r.Header.Get("Idempotency-Key")[:8], "state": "queued"})
	}))
	defer u.Close()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEnabled, legacySpeechFixture.YuEEndpoint, legacySpeechFixture.YuEToken = true, u.URL, "synthetic-token"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	installLegacyLocalMusicPlugin(t, f.manager)
	for _, body := range []string{`{"title":"下班晚风","style":"folk","lyrics":"[Verse] supplied"}`, `{ "lyrics":"[Verse] supplied", "style":"folk", "title":"下班晚风" }`} {
		if _, err := p.callYuE(context.Background(), "music_local_start", json.RawMessage(body)); err != nil {
			t.Fatal(err)
		}
	}
	p.trace.TraceID = "second"
	if _, err := p.callYuE(context.Background(), "music_local_start", json.RawMessage(`{"title":"下班晚风","style":"folk","lyrics":"[Verse] supplied"}`)); err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 2 || seeds[0] == seeds[1] {
		t.Fatal("retry duplicated or new request reused default seed", seeds)
	}
}
func TestMusicReferenceRecipeReadAndGeneratedCoverName(t *testing.T) {
	f := setup(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "result"})
	spec := &songSpec{Mode: "cover", Style: "female folk", Lyrics: "supplied lyrics", Seed: 123, Reference: "asset-original", ReferenceMode: "melody", Duration: 60}
	if _, err := p.stageSongOutputSpec(context.Background(), []byte("fLaC synthetic output"), "audio/flac", "原曲女声版", "file", spec); err != nil {
		t.Fatal(err)
	}
	assets, err := f.manager.loadAssets()
	if err != nil || len(assets) != 1 || assets[0].Music == nil || assets[0].Filename != "大肥鱼翻唱-原曲女声版.flac" {
		t.Fatal(assets, err)
	}
	raw, _ := json.Marshal(map[string]string{"asset_id": assets[0].ID})
	result, err := p.inspectMusicReference(context.Background(), raw)
	if err != nil || !strings.Contains(result, "supplied lyrics") || !strings.Contains(result, `"seed":123`) {
		t.Fatal(result, err)
	}
	p.trace.Thread = "other-room"
	if _, err = p.inspectMusicReference(context.Background(), raw); err == nil {
		t.Fatal("cross room source readable")
	}
}
