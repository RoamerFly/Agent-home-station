package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeLocalASRMigrationSource(t *testing.T, f *webFixture, profile, backend string, enabled bool) []byte {
	t.Helper()
	f.manager.mu.Lock()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.ASREnabled, legacySpeechFixture.ASRBackend, legacySpeechFixture.ASRLocalModel = enabled, backend, profile
		legacySpeechFixture.Key = "synthetic-legacy-asr-key"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	f.manager.config.APIKey = "synthetic-legacy-vision-key"
	speech := readLegacySpeechTestConfig(t, f.manager)
	f.manager.mu.Unlock()
	raw, err := json.Marshal(speech)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(f.dir, "speech-config.json"), raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPluginMigrationLocalASRProfileMapsIntoRealConfigurationButUnverifiedApplyIsBlocked(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, test := range []struct{ name, legacy, wanted string }{
		{"default", "", "sensevoice-small-int8"},
		{"custom", "sensevoice-synthetic-custom", "sensevoice-synthetic-custom"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			original := writeLocalASRMigrationSource(t, f, test.legacy, "local", true)
			preview, err := f.manager.pluginMigrationPreview(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, "media-understanding")
			if step.ProfileID != test.wanted || string(step.Values["local_asr_profile_id"]) != strconv.Quote(test.wanted) || string(step.update.Values["local_asr_profile_id"]) != strconv.Quote(test.wanted) || string(step.update.Values["asr_backend"]) != `"local"` {
				t.Fatal("local profile was not carried from preview into apply configuration", step)
			}
			if step.Status != "blocked" || step.ProposedEnabled || step.EligibleToEnable || !migrationStringContains(step.RequiredMissing, "registered_local_asr_profile:"+test.wanted) {
				t.Fatal("unverified local resources were treated as ready", step)
			}
			if _, _, _, err := f.manager.applyPluginMigration(t.Context(), "test", step.Plugin, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
				t.Fatal("actual migration bypassed missing local resource verification")
			}
			if _, err := os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
				t.Fatal("blocked migration created a store", err)
			}
			// Validate the exact proposed ConfigUpdate against the actual official
			// package schema, under a separate explicit test installation. This
			// does not remove the preview's resource prerequisite or enable it.
			store, err := f.manager.packageStore()
			if err != nil {
				t.Fatal(err)
			}
			directory, err := officialMigrationDirectory(step.Plugin)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := store.Prepare(t.Context(), directory)
			if err != nil {
				t.Fatal(err)
			}
			installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
			if err != nil {
				t.Fatal(err)
			}
			config, err := store.SaveConfiguration(t.Context(), installed.Package.ID, step.update)
			if err != nil || string(config.Values["local_asr_profile_id"]) != strconv.Quote(test.wanted) || string(config.Values["asr_backend"]) != `"local"` {
				t.Fatal("official Store rejected or lost proposed local profile configuration", config, err)
			}
			installed, err = store.Installed(t.Context(), installed.Package.ID)
			if err != nil || installed.Enabled || installed.Removed {
				t.Fatal("schema validation activated the package", installed, err)
			}
			for _, name := range config.ConfiguredSecrets {
				if name == "asr_key" {
					t.Fatal("local migration copied an unused cloud ASR key")
				}
			}
			after, err := os.ReadFile(filepath.Join(f.dir, "speech-config.json"))
			if err != nil || string(after) != string(original) || readLegacySpeechTestConfig(t, f.manager).ASRLocalModel != test.legacy {
				t.Fatal("profile selection wrote defaults back into legacy settings", err)
			}
		})
	}
}

func TestPluginMigrationASRBackendKeepsExplicitChoiceAndOmitsInactiveProfile(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, test := range []struct {
		name, backend string
		enabled       bool
	}{
		{"remote", "bailian", true}, {"legacy_fallback", "local_then_bailian", true}, {"disabled_local", "local", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			writeLocalASRMigrationSource(t, f, "sensevoice-unused-selection", test.backend, test.enabled)
			preview, err := f.manager.pluginMigrationPreview(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, "media-understanding")
			if _, ok := step.Values["local_asr_profile_id"]; ok {
				t.Fatal("non-local backend mapped a profile", step)
			}
			if _, ok := step.update.Values["local_asr_profile_id"]; ok || step.ProfileID != "" {
				t.Fatal("non-local apply or audit retained an active profile", step)
			}
			if test.backend == "local_then_bailian" {
				if step.Status != "blocked" || !migrationStringContains(step.RequiredMissing, "asr_backend:legacy_local_then_bailian_requires_explicit_choice") || step.Values["asr_backend"] != nil || step.ProposedEnabled {
					t.Fatal("legacy fallback was silently converted to a single backend", step)
				}
			}
			if test.backend == "bailian" && (string(step.update.Values["asr_backend"]) != `"remote"` || step.update.Secrets["asr_key"] != "synthetic-legacy-asr-key") {
				t.Fatal("remote backend mapping changed", step)
			}
		})
	}
}

func TestPluginMigrationLocalASRProfileSourceChangeInvalidatesPlan(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeLocalASRMigrationSource(t, f, "", "local", true)
	first, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := previewStep(t, first, "media-understanding")
	latestSource := writeLocalASRMigrationSource(t, f, "sensevoice-new-selection", "local", true)
	second, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	after := previewStep(t, second, "media-understanding")
	if first.Accounts[0].SourceHashes["speech_config_json"] == second.Accounts[0].SourceHashes["speech_config_json"] || before.ApplySourceHash == after.ApplySourceHash || before.ProfileID == after.ProfileID || !strings.Contains(string(after.update.Values["local_asr_profile_id"]), "sensevoice-new-selection") {
		t.Fatal("profile source change did not invalidate the plan", before, after)
	}
	if _, _, _, err := f.manager.applyPluginMigration(t.Context(), "test", after.Plugin, before.ApplySourceHash, before.Package.ExpectedDigest, before.Permissions); err == nil {
		t.Fatal("old profile plan was applied after source changed")
	}
	actualSource, err := os.ReadFile(filepath.Join(f.dir, "speech-config.json"))
	if err != nil || string(actualSource) != string(latestSource) {
		t.Fatal("preview modified legacy source while hashing selection", err)
	}
}
