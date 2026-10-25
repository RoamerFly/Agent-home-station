package deviceprofile

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func generationInput() GenerateOptions {
	return GenerateOptions{LoginDeviceID: "test-login-id", Now: time.Date(2026, 10, 24, 10, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600)), Location: time.FixedZone("Asia/Shanghai", 8*3600), UUIDEntropy: bytes.NewReader(bytes.Repeat([]byte{0x36}, 64)), Random: rand.New(rand.NewSource(12345))}
}

func TestIdentityPairKeepsNativeOverlappingDigestSlices(t *testing.T) {
	a, b := IdentityPair("test-login-id")
	// Independent Python hashlib MD5 against the exact ABI slice offsets.
	if a != "aa4245a1-4245-245a-a424-06412511439d" || b != "8296bbd3-96bb-6bbd-296b-d831e4873dec" {
		t.Fatal("native UUID-derived identifiers changed")
	}
}

func TestProfileGenerationIsPersistableAndKeepsSeparateIdentities(t *testing.T) {
	in := generationInput()
	p, err := Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.DeviceID == p.LoginDeviceID || p.LoginDeviceID != in.LoginDeviceID || len(p.SDI) != 32 || len(p.GUID1) != 36 || p.Model != "iPad15,6" || p.CoreCount != 3 {
		t.Fatal("identity or frozen profile changed")
	}
	if p.Var.Inode != 2 || p.Var.Access != p.Var.Birth || p.Var.Modified != p.Var.Changed || p.SystemVersion.Access != p.SystemVersion.Changed || p.SystemVersion.Access.Nanos != 0 {
		t.Fatal("native file-stat relations lost")
	}
	install := time.Unix(int64(p.InstallTime), 0).In(in.Location)
	if install.Before(time.Date(2025, 4, 30, 0, 0, 0, 0, in.Location)) || !install.Before(time.Date(2025, 5, 30, 23, 59, 59, 0, in.Location)) {
		t.Fatal("install distribution out of interval")
	}
	boot := time.Unix(int64(p.BootTime), 0)
	if boot.Before(in.Now.AddDate(0, 0, -7)) || boot.After(in.Now.AddDate(0, 0, -6)) {
		t.Fatal("boot distribution out of recovered first-day interval")
	}
	if !strings.HasPrefix(p.Filesystem.MountFrom, "com.apple.os.update-{") || p.Filesystem.TypeName != "apfs" {
		t.Fatal("APFS mount identity changed")
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var restored Profile
	if err = json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(restored)
	if !bytes.Equal(b, b2) {
		t.Fatal("profile changed on persistence roundtrip")
	}
	p2, err := Generate(generationInput())
	if err != nil {
		t.Fatal(err)
	}
	b2, _ = json.Marshal(p2)
	if !bytes.Equal(b, b2) {
		t.Fatal("explicit generation inputs are not deterministic")
	}
}

func TestProfileGenerationRequiresExplicitInputs(t *testing.T) {
	if _, err := Generate(GenerateOptions{}); err == nil {
		t.Fatal("implicit identity generation accepted")
	}
	in := generationInput()
	in.UUIDEntropy = bytes.NewReader(nil)
	if _, err := Generate(in); err == nil {
		t.Fatal("entropy exhaustion ignored")
	}
}
