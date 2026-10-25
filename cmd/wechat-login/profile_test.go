package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileCommandKeepsPrivateMaterialOutOfOutput(t *testing.T) {
	dir := t.TempDir()
	device := filepath.Join(dir, "device.bin")
	state := filepath.Join(dir, "profile.session.json")
	id := append([]byte{0x49}, bytes.Repeat([]byte{0x51}, 15)...)
	if err := os.WriteFile(device, id, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := profileCommand([]string{"-device", device, "-state", state}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), hex.EncodeToString(id)) || strings.Contains(out.String(), base64.StdEncoding.EncodeToString(id)) {
		t.Fatal("login identity printed")
	}
	s, err := loadProfileCheckpoint(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s.LoginDeviceBytes, id) || s.Token != nil {
		t.Fatal("unexpected login or token state")
	}
	if err = profileCommand([]string{"-device", device, "-state", state}, &out); err == nil {
		t.Fatal("existing profile overwritten")
	}
	s.Profile.LoginDeviceID = "different-profile"
	if err = saveProfileCheckpoint(state, s); err != nil {
		t.Fatal(err)
	}
	if _, err = loadProfileCheckpoint(state); err == nil {
		t.Fatal("mismatched profile identity accepted")
	}
}
