package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNamedSpeechMigrationManifest creates only public synthetic registration
// metadata and placeholder resources.  It never creates a voice catalogue or
// a private model/reference, because migration proof must not depend on either.
func writeNamedSpeechMigrationManifest(t *testing.T, m *Manager) (public, manifest, python string) {
	t.Helper()
	public = filepath.Join(m.managedRoot(), "public_workspace")
	project := filepath.Join(public, "projects", "gpt-sovits")
	models := filepath.Join(public, "models", "gpt-sovits")
	python = filepath.Join(public, "runtimes", "gpt-sovits", "python")
	for _, dir := range []string{project, models, filepath.Dir(python), filepath.Join(public, "manifests")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(python, []byte("synthetic managed interpreter"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest = filepath.Join(public, "manifests", "gpt-sovits.json")
	raw, err := json.Marshal(map[string]string{"id": "gpt-sovits", "root": project, "python": python, "base_models": models})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return public, manifest, python
}

func configureNamedSpeechMigrationLegacy(t *testing.T, f *webFixture) []byte {
	t.Helper()
	legacy := f.manager.speechConfig()
	// The local named route is deliberately independent from the cloud default.
	legacy.TTSEnabled, legacy.Key = false, ""
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "speech-config.json"), raw); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.speech = legacy
	f.manager.mu.Unlock()
	return raw
}

func TestNamedSpeechMigrationUsesVerifiedPublicManifestOnly(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	legacy := configureNamedSpeechMigrationLegacy(t, f)
	public, _, _ := writeNamedSpeechMigrationManifest(t, f.manager)
	resources := migrationRegistrationSnapshot(t, public)

	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "speech")
	if step.Status != "ready_for_administrator_review" || !step.LegacyEnabled || !step.EligibleToEnable || step.ProposedEnabled || string(step.Values["local_profile_id"]) != `"gpt-sovits"` || string(step.update.Values["local_profile_id"]) != `"gpt-sovits"` || step.ResourceProof == nil || step.ResourceProof.Source != "gpt-sovits-manifest" || !step.ResourceProof.Present || len(step.ResourceProof.VersionSHA256) != 64 || preview.Accounts[0].SourceHashes["named_speech_resource_version"] != step.ResourceProof.VersionSHA256 {
		t.Fatalf("named resource was not safely mapped: %#v %#v", step, preview.Accounts[0].SourceHashes)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), public) || strings.Contains(string(encoded), "gpt-sovits/python") {
		t.Fatalf("private filesystem details leaked in preview: %s", encoded)
	}
	assertMigrationStoreAbsent(t, f.manager)
	if got := migrationRegistrationSnapshot(t, public); !equalStringMap(resources, got) {
		t.Fatal("read-only named migration changed public registration")
	}

	installed, config, _, err := f.manager.applyPluginMigration(context.Background(), "test", "speech", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
	if err != nil || installed.Enabled || string(config.Values["local_profile_id"]) != `"gpt-sovits"` {
		t.Fatalf("named speech migration did not install disabled exact profile: installed=%+v config=%+v err=%v", installed, config, err)
	}
	if source, readErr := os.ReadFile(filepath.Join(f.dir, "speech-config.json")); readErr != nil || string(source) != string(legacy) {
		t.Fatal("migration changed legacy speech configuration", readErr)
	}
}

func TestNamedSpeechMigrationProofChangeInvalidatesApproval(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	configureNamedSpeechMigrationLegacy(t, f)
	public, _, python := writeNamedSpeechMigrationManifest(t, f.manager)
	first, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := previewStep(t, first, "speech")
	if before.ResourceProof == nil || before.Status != "ready_for_administrator_review" {
		t.Fatal("synthetic named registration was not verified", before)
	}
	if err = os.WriteFile(python, []byte("synthetic managed interpreter changed"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	after := previewStep(t, second, "speech")
	if after.ResourceProof == nil || before.ApplySourceHash == after.ApplySourceHash || before.ResourceProof.VersionSHA256 == after.ResourceProof.VersionSHA256 || first.Accounts[0].SourceHashes["named_speech_resource_version"] == second.Accounts[0].SourceHashes["named_speech_resource_version"] {
		t.Fatalf("changed managed resource did not invalidate approval: before=%#v after=%#v", before, after)
	}
	if _, _, _, err = f.manager.applyPluginMigration(t.Context(), "test", "speech", before.ApplySourceHash, before.Package.ExpectedDigest, before.Permissions); err == nil {
		t.Fatal("stale named speech approval was applied")
	}
	assertMigrationStoreAbsent(t, f.manager)
	// Keep this assertion independent from path contents in the public preview.
	sum := sha256.Sum256([]byte("synthetic managed interpreter changed"))
	if hex.EncodeToString(sum[:]) == after.ResourceProof.VersionSHA256 || resourcesContainPath(t, public, f.dir) {
		t.Fatal("resource proof became a raw file hash or public resources escaped")
	}
}

func TestNamedSpeechMigrationWithoutRegisteredManifestKeepsCloudPreview(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	legacy := configureNamedSpeechMigrationLegacy(t, f)
	preview, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "speech")
	if _, ok := step.Values["local_profile_id"]; ok || step.ResourceProof != nil || step.LegacyEnabled || step.EligibleToEnable || step.Status != "ready_for_administrator_review" {
		t.Fatal("missing local registration changed cloud-only migration preview", step)
	}
	if source, readErr := os.ReadFile(filepath.Join(f.dir, "speech-config.json")); readErr != nil || string(source) != string(legacy) {
		t.Fatal("preview changed cloud legacy configuration", readErr)
	}
}

func TestNamedSpeechMigrationDoesNotInferProfileWithoutDeclaredContract(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	configureNamedSpeechMigrationLegacy(t, f)
	writeNamedSpeechMigrationManifest(t, f.manager)
	packages := migrationCatalog()
	pkg := packages["speech"]
	pkg.namedSpeech = false // fixture for a pre-0.2 package contract.
	packages["speech"] = pkg
	account, err := f.manager.pluginMigrationPreviewFor(t.Context(), packages)
	if err != nil {
		t.Fatal(err)
	}
	var step pluginMigrationStep
	for _, item := range account.Plugins {
		if item.Plugin == "speech" {
			step = item
			break
		}
	}
	if _, ok := step.Values["local_profile_id"]; ok || step.ResourceProof != nil || step.Status != "blocked" || !migrationStringContains(step.RequiredMissing, "official_named_speech_adapter_not_declared_by_speech_plugin") {
		t.Fatal("unreviewed package contract inferred named speech support", step)
	}
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func resourcesContainPath(t *testing.T, public, forbidden string) bool {
	t.Helper()
	contains := false
	if err := filepath.WalkDir(public, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contains = contains || strings.Contains(string(raw), forbidden)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return contains
}
