package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopStopUsesPrivatePipeAndEOF(t *testing.T) {
	for _, input := range []string{"stop\n", "ignored\nstop\n", ""} {
		select {
		case <-desktopStop(strings.NewReader(input)):
		case <-time.After(time.Second):
			t.Fatal("desktop shutdown pipe blocked")
		}
	}
}

func TestNativePendingReadOnlyAcrossAccountsAndCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "合成 native data")
	for _, account := range []string{"one", "two"} {
		dir := filepath.Join(root, "accounts", account, "agent")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(dir, "control.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec("CREATE TABLE media_jobs(state TEXT); INSERT INTO media_jobs VALUES ('queued'),('running'),('accepted')")
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if count, err := nativePending(root); err != nil || count != 4 {
		t.Fatalf("pending=%d error=%v", count, err)
	}
	if _, err := nativePending("relative"); err == nil {
		t.Fatal("relative root admitted")
	}
	if err := os.WriteFile(filepath.Join(root, "accounts", "two", "agent", "control.sqlite"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativePending(root); err == nil {
		t.Fatal("corruption interpreted as idle")
	}
	if count, err := nativePending(t.TempDir()); err != nil || count != 0 {
		t.Fatal("fresh root unavailable", count, err)
	}
}
