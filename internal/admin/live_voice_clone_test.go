package admin

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/mcptools"
)

// Explicit local audition only. CI skips this; it never changes the active voice
// or sends WeChat messages. Reference audio and registry remain private.
func TestLiveBailianReferenceVoiceVariants(t *testing.T) {
	root, workspace, reference := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE"), os.Getenv("WECHAT_VOICE_CLONE_REFERENCE")
	if root == "" || workspace == "" || reference == "" || os.Getenv("WECHAT_VOICE_CLONE_LIVE") != "1" {
		t.Skip("explicit owner-requested reference voice audition")
	}
	// Do this before any paid clone/inspection request. The audition must not
	// create a voice when the official isolated speech route is unavailable.
	_ = requireLiveSpeechPluginRuntime(t)
	cfg, err := loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Model       string `json:"target_model"`
		Preview     string `json:"preview_text"`
		Enrollments []struct {
			Code, Prefix string
			Preprocess   bool
			Normalize    bool `json:"normalize_volume"`
		} `json:"enrollments"`
		Variants []struct{ Code, Name, Enrollment, Instruction string } `json:"variants"`
	}
	b, err := os.ReadFile(filepath.Join(workspace, "design/voice/bluewhale-reference-variants.json"))
	if err != nil || json.Unmarshal(b, &spec) != nil {
		t.Fatal("voice specification unavailable", err)
	}
	sample, err := os.ReadFile(reference)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(sample)
	fingerprint := hex.EncodeToString(hash[:])
	cfg.TTSModel = spec.Model
	client := bailian.New(cfg)
	output := filepath.Join(workspace, "research/evidence/voice-clone-bailian")
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(workspace, "secrets/bluewhale-voice-clone.json")
	entries := map[string]map[string]any{}
	if b, err = os.ReadFile(ledger); err == nil {
		if json.Unmarshal(b, &entries) != nil {
			t.Fatal("private registry invalid")
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	save := func() {
		t.Helper()
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = writePrivate(ledger, b); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range spec.Enrollments {
		entry, exists := entries[profile.Code]
		if exists && (entry["reference_sha256"] != fingerprint || entry["target_model"] != spec.Model || entry["preprocess"] != profile.Preprocess || entry["normalize_volume"] != profile.Normalize) {
			t.Fatal("existing clone recipe differs; inspect registry before creating")
		}
		if !exists {
			entry = map[string]any{"reference_sha256": fingerprint, "target_model": spec.Model, "preprocess": profile.Preprocess, "normalize_volume": profile.Normalize, "state": "creating", "created_at": time.Now().UTC().Format(time.RFC3339)}
			entries[profile.Code] = entry
			save()
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			voice, createErr := client.CloneVoice(ctx, profile.Prefix, sample, bailian.CloneOptions{Preprocess: profile.Preprocess, NormalizeVolume: profile.Normalize})
			cancel()
			entry["voice_id"], entry["state"] = voice.ID, "created"
			save()
			if createErr != nil {
				entry["error"] = createErr.Error()
				save()
				t.Fatal("clone not retried", profile.Code, createErr)
			}
		}
		id, _ := entry["voice_id"].(string)
		if id == "" {
			t.Fatal("uncertain previous creation; refusing duplicate", profile.Code)
		}
		// Query is read-only; a newly enrolled voice may briefly be deploying.
		for attempt := 0; attempt < 12; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, err = client.InspectDesignedVoice(ctx, id)
			cancel()
			if err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			t.Fatal("created voice not ready", profile.Code, err)
		}
		entry["state"] = "ready"
		delete(entry, "error")
		save()
		t.Log("clone ready", profile.Code)
	}
	for _, variant := range spec.Variants {
		filename := filepath.Join(output, variant.Code+"-"+variant.Name+".wav")
		if info, err := os.Stat(filename); err == nil && info.Size() > 44 {
			t.Log("reuse audition", variant.Code)
			continue
		}
		id, _ := entries[variant.Enrollment]["voice_id"].(string)
		voiceCfg := cfg
		voiceCfg.Voice = id
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		url, synthErr := livePluginSynthesize(t, ctx, voiceCfg, spec.Preview, variant.Instruction)
		var audio []byte
		if synthErr == nil {
			audio, _, synthErr = mcptools.GetPublicMedia(ctx, url, 8<<20)
		}
		cancel()
		if synthErr != nil {
			t.Fatal("audition failed", variant.Code, synthErr)
		}
		if len(audio) < 44 || string(audio[:4]) != "RIFF" || string(audio[8:16]) != "WAVEfmt " || string(audio[36:40]) != "data" || (len(audio)-44)%2 != 0 {
			t.Fatal("unsupported audition WAV", variant.Code)
		}
		if binary.LittleEndian.Uint32(audio[40:44]) == 0x7fffff9b {
			binary.LittleEndian.PutUint32(audio[4:8], uint32(len(audio)-8))
			binary.LittleEndian.PutUint32(audio[40:44], uint32(len(audio)-44))
		}
		if err = os.WriteFile(filename, audio, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("audition ready", variant.Code, variant.Name, len(audio))
	}
	// Observe the reference text privately; recognition is not a similarity score.
	transcriptPath := filepath.Join(output, "reference-transcript.txt")
	if _, err = os.Stat(transcriptPath); os.IsNotExist(err) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		text, asrErr := client.Transcribe(ctx, sample)
		cancel()
		if asrErr == nil {
			if err = os.WriteFile(transcriptPath, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Log("optional reference ASR unavailable", asrErr)
		}
	}
}
