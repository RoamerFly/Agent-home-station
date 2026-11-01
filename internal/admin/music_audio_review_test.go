package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestSongAudioSamplesUseActualShortRecordingAndRejectNonAudio(t *testing.T) {
	root := t.TempDir()
	wav, windows, err := prepareSongAudioSamples(t.Context(), root, syntheticReferenceWAV(t, 4))
	if err != nil || len(wav) < 44 || len(wav) > 3<<20 || len(windows) != 3 {
		t.Fatal("invalid generated-song samples", err, len(wav), windows)
	}
	for i, window := range windows {
		if window.Offset < 0 || window.Offset >= 4 || window.Seconds <= 0 || window.Seconds > 15 || window.Offset+window.Seconds > 4.00001 {
			t.Fatal("sample window overstated actual short recording", i, window)
		}
	}
	if _, _, err = prepareSongAudioSamples(t.Context(), root, []byte("not real audio")); err == nil {
		t.Fatal("non-audio became listening evidence")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("sampling left generated raw files behind", entries, err)
	}
}

func TestSongAudioReviewMissingPluginIgnoresLegacyKeyAndBareJSONCache(t *testing.T) {
	f, p, audio, spec, origin := songAudioFixture(t, false, false)
	f.manager.speech.Key = "synthetic-legacy-key"
	cache := filepath.Join(f.dir, "music-audio-reviews")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	legacyHash := sha256.New()
	legacyHash.Write([]byte("song-listening-v1|female|qwen3.8-omni-flash|"))
	legacyHash.Write(audio)
	legacyHash.Write([]byte(f.manager.speech.Endpoint))
	path := filepath.Join(cache, hex.EncodeToString(legacyHash.Sum(nil))+".json")
	raw := []byte(`{"state":"observed","model":"legacy-default","observation":"fabricated old cached review"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	review, check := p.reviewSongAudio(t.Context(), audio, "female", origin.task.MusicJob, spec, "file")
	if review["state"] != "unavailable" || review["observation"] != nil || check == nil || check(t.Context()) != nil {
		t.Fatal("legacy key/cache bypassed missing audio plugin", review)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("existing user review cache was modified", err)
	}
	f.manager.config.Companion.Permissions.SendFile = false
	if err = check(t.Context()); err == nil {
		t.Fatal("unavailable observation lost original task permission guard")
	}
}
