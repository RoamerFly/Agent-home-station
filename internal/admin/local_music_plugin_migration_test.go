package admin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func writeLocalMusicMigrationLegacy(t *testing.T, f *webFixture, enabled bool, endpoint, token string) {
	t.Helper()
	legacy := readLegacySpeechTestConfig(t, f.manager)
	legacy.YuEEnabled, legacy.YuEEndpoint, legacy.YuEToken = enabled, endpoint, token
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "speech-config.json"), data); err != nil {
		t.Fatal(err)
	}
	writeLegacySpeechMigrationFixture(t, f.manager, legacy)
}

func TestLocalMusicPluginMigrationMapsReviewedBridgeWithoutCloudKey(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeLocalMusicMigrationLegacy(t, f, true, "http://127.0.0.1:8765/", "synthetic-local-music-token")
	legacy := readLegacySpeechTestConfig(t, f.manager)
	legacy.MusicEnabled, legacy.Key = true, ""
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "speech-config.json"), data); err != nil {
		t.Fatal(err)
	}
	writeLegacySpeechMigrationFixture(t, f.manager, legacy)

	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "music")
	if step.Status != "ready_for_administrator_review" || !step.LegacyEnabled || !step.EligibleToEnable || step.ProposedEnabled || string(step.Values["local_endpoint"]) != `"http://127.0.0.1:8765"` || step.update.Secrets["local_token"] != "synthetic-local-music-token" || migrationStringContains(step.RequiredMissing, "music_key") || !step.Secrets["local_token"].Present || step.Secrets["local_token"].SourceField != "speech-config.json:yue_token" {
		t.Fatal("local bridge preview lost its independent configuration", step)
	}
	if _, _, _, err = f.manager.applyPluginMigration(context.Background(), "test", "music", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err != nil {
		t.Fatal("valid local-only migration was not installed disabled", err)
	}
}

func TestLocalMusicPluginMigrationFailsClosedForUndeclaredOrUnsafeBridge(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, tc := range []struct {
		name, endpoint, required string
		declared                 bool
	}{
		{name: "missing bridge declaration", endpoint: "http://127.0.0.1:8765", required: "official_local_music_bridge_not_declared_by_music_plugin"},
		{name: "unsafe public http endpoint", endpoint: "http://music.example", required: "local_endpoint_invalid", declared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			writeLocalMusicMigrationLegacy(t, f, true, tc.endpoint, "synthetic-local-token")
			packages := migrationCatalog()
			if !tc.declared {
				pkg := packages["music"]
				pkg.localMusic = false
				packages["music"] = pkg
			}
			account, err := f.manager.pluginMigrationPreviewFor(context.Background(), packages)
			if err != nil {
				t.Fatal(err)
			}
			var step pluginMigrationStep
			for _, candidate := range account.Plugins {
				if candidate.Plugin == "music" {
					step = candidate
				}
			}
			if step.Status != "blocked" || !migrationStringContains(step.RequiredMissing, tc.required) || step.update.Values["local_endpoint"] != nil || step.update.Secrets["local_token"] != "" {
				t.Fatal("unsafe or undeclared local bridge entered configuration", step)
			}
		})
	}
}

func TestLocalMusicPluginMigrationSourceChangeInvalidatesApproval(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeLocalMusicMigrationLegacy(t, f, true, "http://127.0.0.1:8765", "synthetic-original-local-token")
	first, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := previewStep(t, first, "music")
	writeLocalMusicMigrationLegacy(t, f, true, "http://127.0.0.1:8766", "synthetic-replaced-local-token")
	second, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after := previewStep(t, second, "music")
	if before.ApplySourceHash == after.ApplySourceHash || string(after.Values["local_endpoint"]) != `"http://127.0.0.1:8766"` || before.Secrets["local_token"].SHA256 == after.Secrets["local_token"].SHA256 {
		t.Fatal("local endpoint or token change did not invalidate administrator approval", before, after)
	}
}
