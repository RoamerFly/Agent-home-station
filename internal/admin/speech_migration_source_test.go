package admin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/mediaspec"
)

// Legacy fixture settings live only in each test's temporary source file.
func readLegacySpeechTestConfig(t *testing.T, m *Manager) mediaspec.Config {
	t.Helper()
	config := mediaspec.Defaults()
	raw, err := os.ReadFile(filepath.Join(m.dir, "speech-config.json"))
	if os.IsNotExist(err) {
		return config
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func writeLegacySpeechMigrationFixture(t *testing.T, m *Manager, config mediaspec.Config) []byte {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(m.dir, "speech-config.json"), raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLegacySpeechMigrationSourceDoesNotBlockStartup(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, installed := range []bool{false, true} {
		for _, corrupt := range []string{`{"key":"synthetic-private-corrupt",`, `{"asr_backend":"retired-invalid-backend","key":"synthetic-private-corrupt"}`} {
			name := "chat-only"
			if installed {
				name = "installed"
			}
			t.Run(name+"/"+corrupt[:8], func(t *testing.T) {
				dir := t.TempDir()
				if installed {
					seed, err := NewManager(dir, "", &fakeNative{})
					if err != nil {
						t.Fatal(err)
					}
					installDefaultSpeechFixture(t, seed, true, true)
					if err = seed.Close(); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(dir, "speech-config.json")
				original := []byte(corrupt)
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				manager, err := NewManager(dir, "", &fakeNative{})
				if err != nil {
					t.Fatal("legacy migration input blocked startup", err)
				}
				t.Cleanup(func() { _ = manager.Close() })
				if installed && !manager.speechPluginConfigured(t.Context()) {
					t.Fatal("legacy input disabled installed speech configuration")
				}
				if !installed {
					if _, err = os.Stat(filepath.Join(dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
						t.Fatal("chat-only startup opened optional plugin storage", err)
					}
				}
				if _, err = manager.pluginMigrationPreview(t.Context()); err == nil {
					t.Fatal("invalid legacy source silently became a default migration")
				}
				unchanged, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(unchanged, original) {
					t.Fatal("startup or failed preview altered legacy bytes", err)
				}

				repaired := mediaspec.Defaults()
				repaired.TTSEnabled, repaired.Key, repaired.Voice = true, "synthetic-repaired-private-key", "repaired-voice"
				repairedBytes := writeLegacySpeechMigrationFixture(t, manager, repaired)
				first, err := manager.pluginMigrationPreview(t.Context())
				if err != nil {
					t.Fatal("repaired source remained blocked", err)
				}
				before := previewStep(t, first, "speech")
				if string(before.Values["default_voice"]) != `"repaired-voice"` || before.update.Secrets["tts_key"] != repaired.Key || first.Accounts[0].SourceHashes["speech_config_json"] != migrationBytesHash(repairedBytes) {
					t.Fatal("preview did not bind repaired file bytes")
				}
				repaired.Voice, repaired.Key = "changed-file-voice", "synthetic-changed-private-key"
				latest := writeLegacySpeechMigrationFixture(t, manager, repaired)
				second, err := manager.pluginMigrationPreview(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				after := previewStep(t, second, "speech")
				if string(after.Values["default_voice"]) != `"changed-file-voice"` || after.update.Secrets["tts_key"] != repaired.Key || before.ApplySourceHash == after.ApplySourceHash || second.Accounts[0].SourceHashes["speech_config_json"] != migrationBytesHash(latest) {
					t.Fatal("preview paired new source hash with stale values")
				}
				unchanged, err = os.ReadFile(path)
				if err != nil || !bytes.Equal(unchanged, latest) {
					t.Fatal("successful preview altered source bytes", err)
				}
				if !installed {
					if _, err = os.Stat(filepath.Join(dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
						t.Fatal("preview opened optional plugin storage", err)
					}
				}
			})
		}
	}
}
