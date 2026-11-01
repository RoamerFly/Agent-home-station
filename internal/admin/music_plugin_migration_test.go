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
	"time"

	"wechat-bot/internal/companion"
)

func writeMusicMigrationLegacy(t *testing.T, f *webFixture, endpoint, key string) {
	t.Helper()
	legacy := f.manager.speechConfig()
	legacy.MusicEnabled, legacy.Endpoint, legacy.Key, legacy.MusicModel = true, endpoint, key, "synthetic-music-migration"
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "speech-config.json"), data); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.speech = legacy
	f.manager.mu.Unlock()
}

func TestMusicPluginMigrationPreviewApplyPrivateConfigurationAndEnable(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, endpoint := range []string{"https://dashscope-intl.aliyuncs.com", "https://dashscope-intl.aliyuncs.com/"} {
		t.Run(endpoint, func(t *testing.T) {
			f := setup(t)
			const key = "synthetic-private-music-migration-key"
			writeMusicMigrationLegacy(t, f, endpoint, key)
			preview, err := f.manager.pluginMigrationPreview(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, "music")
			wantEndpoint := "https://dashscope-intl.aliyuncs.com/api/v1/services/audio/music/generation"
			if !preview.ReadOnly || step.Status != "ready_for_administrator_review" || !step.LegacyEnabled || step.ProposedEnabled || !step.EligibleToEnable || len(step.RequiredMissing) != 0 || string(step.Values["music_endpoint"]) != strconv.Quote(wantEndpoint) || string(step.Values["music_model"]) != `"synthetic-music-migration"` {
				t.Fatal("unexpected music migration preview", step)
			}
			sum := sha256.Sum256([]byte(key))
			secret := step.Secrets["music_key"]
			public, _ := json.Marshal(preview)
			if bytes.Contains(public, []byte(key)) || !secret.Present || secret.SourceField != "speech-config.json:key" || secret.SHA256 != hex.EncodeToString(sum[:]) || step.update.Secrets["music_key"] != key || len(step.update.Values) != 2 || step.Values["music_key"] != nil {
				t.Fatal("preview leaked or misbound the legacy music key")
			}
			if _, err = os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
				t.Fatal("read-only music preview created the plugin store", err)
			}

			i, config, record, err := f.manager.applyPluginMigration(t.Context(), "test", "music", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
			if err != nil || i.Enabled || i.Removed || record.State != "complete" || record.ConfigRevision != step.ExpectedRevision || config.Revision <= step.ExpectedRevision || record.InstalledRevision != i.Revision || len(config.Values) != 2 || string(config.Values["music_endpoint"]) != strconv.Quote(wantEndpoint) || !migrationStringContains(config.ConfiguredSecrets, "music_key") {
				t.Fatal("music migration did not install valid disabled state", i.Enabled, record.State, err)
			}
			public, _ = json.Marshal(map[string]any{"installed": i, "configuration": config, "migration": record})
			if bytes.Contains(public, []byte(key)) || config.Values["music_key"] != nil {
				t.Fatal("public migration result exposed a private key")
			}
			store, err := f.manager.packageStore()
			if err != nil {
				t.Fatal(err)
			}
			read, err := store.Configuration(t.Context(), "music")
			if err != nil || read.Revision != config.Revision || len(read.Values) != 2 || read.Values["music_key"] != nil || !migrationStringContains(read.ConfiguredSecrets, "music_key") || string(read.Values["music_endpoint"]) != strconv.Quote(wantEndpoint) {
				t.Fatal("stored public configuration differs from the migration result", err)
			}
			if _, err = store.ConfigurationForExecution(t.Context(), "music"); err == nil {
				t.Fatal("migration implicitly enabled execution")
			}
			i, err = store.SetEnabled(t.Context(), "music", i.Revision, true)
			if err != nil || !i.Enabled {
				t.Fatal("migrated music configuration could not enable", err)
			}
			private, err := store.ConfigurationForExecution(t.Context(), "music")
			if err != nil || string(private.Values["music_key"]) != strconv.Quote(key) || private.Revision != config.Revision {
				t.Fatal("host-private execution lost the migrated credential", err)
			}
			plugin, err := store.ConfigurationForPlugin(t.Context(), "music")
			if err != nil || plugin.Values["music_key"] != nil || string(plugin.Values["music_model"]) != `"synthetic-music-migration"` {
				t.Fatal("host-only credential escaped into plugin configuration", err)
			}

			// Exercise the actual host model service after the migrated package is
			// enabled. The callback below is the only provider transport: no HTTP,
			// audio download, isolated process or native WeChat action occurs.
			f.manager.config.Enabled = true
			f.manager.config.Companion.Permissions.SendVoice = true
			access := &permittedTools{manager: f.manager, trace: companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-user", TraceID: "music-migration", Text: "创作一首原创歌"}}
			policy, err := access.newMusicPluginPolicy(t.Context(), json.RawMessage(`{"title":"晚风轻唱","prompt":"synthetic original music","kind":"voice"}`))
			if err != nil {
				t.Fatal("enabled migrated package was not usable by the music policy", err)
			}
			job, cap := musicPluginJob(policy)
			calls := 0
			policy.post = func(_ context.Context, actualEndpoint string, headers map[string]string, body json.RawMessage, _ int64, _ time.Duration) (json.RawMessage, error) {
				calls++
				if actualEndpoint != wantEndpoint || headers["Authorization"] != "Bearer "+key || !bytes.Contains(body, []byte(`"model":"synthetic-music-migration"`)) {
					t.Fatal("migrated model request was not bound to the private configuration")
				}
				return json.RawMessage(`{"model":"synthetic-music-migration","output":{"audio":{"url":"https://audio.example/synthetic.mp3"}}}`), nil
			}
			if _, err = policy.Service(t.Context(), job, cap, "wechat/model_request", musicPluginRequest(policy)); err != nil || calls != 1 {
				t.Fatal("migrated music model service failed", err, calls)
			}
			if _, err = policy.Validate(t.Context(), job, cap, musicPluginOutput("https://audio.example/synthetic.mp3", "synthetic-music-migration")); err != nil {
				t.Fatal("migrated music provider response was not verifiable", err)
			}
		})
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
	if step.Status != "blocked" || step.EligibleToEnable || step.ProposedEnabled || !migrationStringContains(step.RequiredMissing, "music_key") || step.Secrets["music_key"].Present || step.Secrets["music_key"].SHA256 != "" {
		t.Fatal("missing music credential was treated as ready", step)
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
			legacy := f.manager.speechConfig()
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
				f.manager.mu.Lock()
				f.manager.speech = legacy
				f.manager.mu.Unlock()
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
