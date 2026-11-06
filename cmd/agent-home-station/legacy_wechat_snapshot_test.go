package main

import (
	"path/filepath"
	"testing"
)

func TestParseLegacyWechatSnapshotArgs(t *testing.T) {
	legacyData := t.TempDir()
	snapshot := t.TempDir()

	preview, err := parseLegacyWechatSnapshotArgs([]string{
		"--legacy-data", legacyData, "--snapshot", snapshot, "--account", "desktop",
	})
	if err != nil {
		t.Fatalf("preview arguments: %v", err)
	}
	if preview.legacyDataDir != filepath.Clean(legacyData) || preview.snapshotDir != filepath.Clean(snapshot) || preview.accountID != "desktop" || preview.applyDigest != "" || preview.verify {
		t.Fatalf("preview arguments = %#v", preview)
	}

	apply, err := parseLegacyWechatSnapshotArgs([]string{
		"--legacy-data", legacyData, "--snapshot", snapshot, "--account", "desktop", "--apply", "preview-digest",
	})
	if err != nil || apply.applyDigest != "preview-digest" || apply.verify {
		t.Fatalf("apply arguments = %#v, %v", apply, err)
	}

	verify, err := parseLegacyWechatSnapshotArgs([]string{"--snapshot", snapshot, "--verify"})
	if err != nil {
		t.Fatalf("verify arguments: %v", err)
	}
	if !verify.verify || verify.snapshotDir != filepath.Clean(snapshot) || verify.legacyDataDir != "" || verify.accountID != "" {
		t.Fatalf("verify arguments = %#v", verify)
	}
}

func TestParseLegacyWechatSnapshotArgsRejectsAmbiguousInput(t *testing.T) {
	legacyData := t.TempDir()
	snapshot := t.TempDir()
	for _, args := range [][]string{
		{"--legacy-data", "relative", "--snapshot", snapshot, "--account", "desktop"},
		{"--legacy-data", legacyData, "--snapshot", "relative", "--account", "desktop"},
		{"--legacy-data", legacyData, "--snapshot", snapshot},
		{"--snapshot", snapshot, "--verify", "--account", "desktop"},
		{"--snapshot", snapshot, "--verify", "--legacy-data", legacyData},
		{"--snapshot", snapshot, "--verify", "unexpected"},
	} {
		if _, err := parseLegacyWechatSnapshotArgs(args); err == nil {
			t.Fatalf("unsafe snapshot CLI arguments accepted: %#v", args)
		}
	}
}
