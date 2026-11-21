package admin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeMusicMigrationLegacy(t *testing.T, f *webFixture, endpoint, key string) {
	t.Helper()
	legacy := readLegacySpeechTestConfig(t, f.manager)
	legacy.MusicEnabled, legacy.Endpoint, legacy.Key, legacy.MusicModel = true, endpoint, key, "synthetic-music-migration"
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "speech-config.json"), data); err != nil {
		t.Fatal(err)
	}
	writeLegacySpeechMigrationFixture(t, f.manager, legacy)
}

func TestRetiredMusicCloudMigrationIsNotOffered(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	const key = "synthetic-private-music-migration-key"
	writeMusicMigrationLegacy(t, f, "https://dashscope-intl.aliyuncs.com", key)
	preview, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "music")
	if step.Status != "blocked" || step.EligibleToEnable || step.ProposedEnabled {
		t.Fatal("retired cloud music was offered for migration", step)
	}
	public, _ := json.Marshal(preview)
	if bytes.Contains(public, []byte(key)) {
		t.Fatal("retired music preview exposed legacy key")
	}
	if _, _, _, err := f.manager.applyPluginMigration(t.Context(), "test", "music", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
		t.Fatal("retired cloud music migration applied")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
		t.Fatal("retired music migration wrote a plugin store", err)
	}
}

func TestMusicPluginMigrationMissingKeyBlocksBeforeStoreWrite(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeMusicMigrationLegacy(t, f, "https://dashscope.aliyuncs.com", "")
	preview, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "music")
	if step.Status != "blocked" || step.EligibleToEnable || step.ProposedEnabled || !migrationStringContains(step.RequiredMissing, "cloud_music_retired_use_station_yue") || step.Secrets["music_key"].Present || step.Secrets["music_key"].SHA256 != "" {
		t.Fatal("retired cloud music was treated as ready", step)
	}
	if _, _, _, err = f.manager.applyPluginMigration(t.Context(), "test", "music", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
		t.Fatal("music migration applied without its required private credential")
	}
	if _, err = os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
		t.Fatal("blocked music migration wrote a plugin store", err)
	}
}

func TestMusicPluginMigrationRejectsChangedLegacySource(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, change := range []string{"endpoint", "key", "model", "file_only"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			writeMusicMigrationLegacy(t, f, "https://dashscope.aliyuncs.com", "synthetic-original-key")
			preview, err := f.manager.pluginMigrationPreview(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, "music")
			legacy := readLegacySpeechTestConfig(t, f.manager)
			switch change {
			case "endpoint":
				legacy.Endpoint = "https://dashscope-intl.aliyuncs.com"
			case "key":
				legacy.Key = "synthetic-changed-key"
			case "model":
				legacy.MusicModel = "synthetic-changed-model"
			case "file_only":
				if err = writePrivate(filepath.Join(f.dir, "speech-config.json"), []byte("{}")); err != nil {
					t.Fatal(err)
				}
			}
			if change != "file_only" {
				writeLegacySpeechMigrationFixture(t, f.manager, legacy)
			}
			if _, _, _, err = f.manager.applyPluginMigration(t.Context(), "test", "music", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
				t.Fatal("changed legacy music source accepted an old approval", change)
			}
			// A rejected plan must neither install a package nor create the store.
			if _, err = os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
				t.Fatal("stale music plan unexpectedly opened its store", err)
			}
			current, err := f.manager.pluginMigrationPreview(t.Context())
			if err != nil || previewStep(t, current, "music").ApplySourceHash == step.ApplySourceHash {
				t.Fatal("music source change did not invalidate the approval fingerprint", err)
			}
		})
	}
}
