package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/loginflow"
	"wechat-bot/internal/loginwire"
)

func TestUpgradePreservesIdentityAndRequiresFreshToken(t *testing.T) {
	dir := t.TempDir()
	device, from, to := filepath.Join(dir, "device.bin"), filepath.Join(dir, "old.json"), filepath.Join(dir, "new.json")
	id := append([]byte{0x49}, bytes.Repeat([]byte{0x51}, 15)...)
	if err := os.WriteFile(device, id, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := profileCommand([]string{"-device", device, "-state", from}, &out); err != nil {
		t.Fatal(err)
	}
	s, err := loadProfileCheckpoint(from)
	if err != nil {
		t.Fatal(err)
	}
	secret := loginwire.SecretText("synthetic-private-token")
	s.Token, s.TokenAttemptedAt = &loginwire.TrustResponse{Token: &secret}, 123
	if err = saveProfileCheckpoint(from, s); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(from)
	wire := loginflow.DefaultWireProfile()
	wire.Version += 256 // synthetic propagation check, not a claimed accepted version.
	c := UpgradeCandidate{Schema: "wechat-ipad-upgrade-candidate-1", Name: "synthetic-offline", SourceURL: "https://example.invalid/static-fixture", ArtifactSHA256: strings.Repeat("a", 64), Wire: wire}
	b, _ := json.Marshal(c)
	candidate := filepath.Join(dir, "candidate.json")
	if err = os.WriteFile(candidate, b, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = upgradeCommand([]string{"-from", from, "-out", to, "-candidate", candidate}, &out); err != nil {
		t.Fatal(err)
	}
	upgraded, err := loadProfileCheckpoint(to)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Token != nil || upgraded.TokenAttemptedAt != 0 || len(upgraded.TokenHistory) != 0 {
		t.Fatal("old token survived protocol change")
	}
	if !bytes.Equal(upgraded.LoginDeviceBytes, s.LoginDeviceBytes) || upgraded.Profile.DeviceID != s.Profile.DeviceID || upgraded.Profile.UUIDOne != s.Profile.UUIDOne || upgraded.Profile.InstallTime != s.Profile.InstallTime || *upgraded.Profile.Var != *s.Profile.Var {
		t.Fatal("protocol migration changed persistent device identity or statistics")
	}
	if upgraded.Profile.ClientVersion != wire.Version || upgraded.WireProfile == nil || *upgraded.WireProfile != wire || upgraded.Upgrade == nil || len(upgraded.Upgrade.CandidateSHA256) != 64 {
		t.Fatal("candidate not checkpointed")
	}
	after, _ := os.ReadFile(from)
	if !bytes.Equal(after, original) || strings.Contains(out.String(), string(secret)) {
		t.Fatal("source changed or private token printed")
	}
	if err = upgradeCommand([]string{"-from", from, "-out", to, "-candidate", candidate}, &out); err == nil {
		t.Fatal("existing output overwritten")
	}
	if err = upgradeCommand([]string{"-from", to, "-out", filepath.Join(dir, "second.json"), "-candidate", candidate}, &out); err == nil {
		t.Fatal("same version accepted as upgrade")
	}
}
