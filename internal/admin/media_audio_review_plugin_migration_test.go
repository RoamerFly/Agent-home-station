package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func writeAudioReviewMigrationLegacy(t *testing.T, f *webFixture, endpoint, key string) {
	t.Helper()
	legacy := f.manager.speechConfig()
	legacy.Endpoint, legacy.Key = endpoint, key
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "speech-config.json"), b); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.speech = legacy
	f.manager.mu.Unlock()
}

func TestMediaAudioReviewMigrationPreviewApplyDisabled(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	const endpoint = "https://dashscope-intl.aliyuncs.com/"
	const key = "synthetic-private-audio-review-migration-key"
	writeAudioReviewMigrationLegacy(t, f, endpoint, key)

	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "media-understanding")
	wantEndpoint := "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions"
	if step.LegacyEnabled || step.ProposedEnabled || step.EligibleToEnable || step.Status != "ready_for_administrator_review" || len(step.RequiredMissing) != 0 || string(step.Values["audio_review_endpoint"]) != strconv.Quote(wantEndpoint) || string(step.Values["audio_review_model"]) != `"qwen3.8-omni-flash"` {
		t.Fatalf("unexpected audio review preview: %#v", step)
	}
	sum := sha256.Sum256([]byte(key))
	secret := step.Secrets["audio_review_key"]
	public, _ := json.Marshal(preview)
	if bytes.Contains(public, []byte(key)) || !secret.Present || secret.SourceField != "speech-config.json:key" || secret.SHA256 != hex.EncodeToString(sum[:]) || step.update.Secrets["audio_review_key"] != key || step.Values["audio_review_key"] != nil {
		t.Fatal("audio review key leaked or was not kept host-only")
	}
	if _, err = os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
		t.Fatal("read-only preview created plugin state", err)
	}

	installed, config, record, err := f.manager.applyPluginMigration(context.Background(), "test", "media-understanding", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
	if err != nil || installed.Enabled || installed.Removed || record.State != "complete" || string(config.Values["audio_review_endpoint"]) != strconv.Quote(wantEndpoint) || string(config.Values["audio_review_model"]) != `"qwen3.8-omni-flash"` || config.Values["audio_review_key"] != nil || !migrationStringContains(config.ConfiguredSecrets, "audio_review_key") {
		t.Fatalf("audio review migration did not create a disabled host-private configuration: installed=%+v config=%+v record=%+v err=%v", installed, config, record, err)
	}
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfigurationForExecution(context.Background(), "media-understanding"); err == nil {
		t.Fatal("audio review migration implicitly enabled execution")
	}
}

func TestMediaAudioReviewMigrationEmptyKeyDoesNotBlockVision(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	config := f.manager.config
	config.APIKey = "synthetic-vision-key-that-is-long-enough"
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "config.json"), b); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.config = config
	f.manager.mu.Unlock()
	writeAudioReviewMigrationLegacy(t, f, "https://dashscope.aliyuncs.com", "")

	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "media-understanding")
	if step.Status != "ready_for_administrator_review" || len(step.RequiredMissing) != 0 || !step.LegacyEnabled || !step.EligibleToEnable || step.Values["audio_review_endpoint"] != nil || step.Values["audio_review_model"] != nil || step.Secrets["audio_review_key"].Present || step.update.Secrets["audio_review_key"] != "" {
		t.Fatalf("empty legacy speech key changed the existing vision migration: %#v", step)
	}
}

func TestMediaAudioReviewMigrationRejectsChangedLegacySource(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeAudioReviewMigrationLegacy(t, f, "https://dashscope.aliyuncs.com", "synthetic-original-audio-review-key")
	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "media-understanding")
	writeAudioReviewMigrationLegacy(t, f, "https://dashscope.aliyuncs.com", "synthetic-changed-audio-review-key")
	if _, _, _, err = f.manager.applyPluginMigration(context.Background(), "test", "media-understanding", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
		t.Fatal("changed legacy audio review source accepted an old approval")
	}
	if _, err = os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
		t.Fatal("stale audio review plan created plugin state", err)
	}
	current, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil || previewStep(t, current, "media-understanding").ApplySourceHash == step.ApplySourceHash {
		t.Fatal("speech source change did not invalidate the audio review plan", err)
	}
}
