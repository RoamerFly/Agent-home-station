package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/hostfile"
)

// TestCaptureLegacyWechatSnapshotWindows exercises the new, import-compatible
// snapshot boundary without starting a desktop, Agent, native WeChat process,
// model, or network service. The fixture deliberately contains credentials
// and private-looking message bodies: credentials stay behind; message bodies
// stay byte-for-byte in this private, import-only snapshot.
func TestCaptureLegacyWechatSnapshotWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the legacy desktop snapshot contract is Windows-only")
	}
	ctx := context.Background()
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy-v2")
	snapshot := filepath.Join(base, "snapshot")
	fixture := newLegacyWechatSnapshotFixture(t, legacy)

	// Preview is read-only.  It returns a one-shot digest but creates neither
	// the destination nor a durable handover claim.
	beforeConfig := mustReadSnapshotFixture(t, filepath.Join(legacy, "admin", "config.json"))
	preview, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{
		LegacyDataDir:  legacy,
		SnapshotDir:    snapshot,
		PrimaryAccount: fixture.primary,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.State != "ready" || len(preview.ApplyDigest) != 64 || preview.ApplyDigest == strings.Repeat("0", 64) {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if preview.Files == 0 || preview.Accounts != 2 || preview.MemoryFiles != 2 || preview.DeliveryRecords != 2 {
		t.Fatalf("unexpected preview inventory: %#v", preview)
	}
	if public, marshalErr := json.Marshal(preview); marshalErr != nil || bytes.Contains(public, []byte("private old body")) {
		t.Fatalf("snapshot preview exposed delivery content: %s, %v", public, marshalErr)
	}
	if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created target: %v", err)
	}
	if got := mustReadSnapshotFixture(t, filepath.Join(legacy, "admin", "config.json")); !bytes.Equal(got, beforeConfig) {
		t.Fatal("preview changed the legacy configuration")
	}
	if _, err := os.Stat(filepath.Join(legacy, legacyCutoverClaimFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created a legacy claim: %v", err)
	}

	// A source change invalidates the original approval token.  The failed
	// apply must not partially publish a target tree.
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "admin", "config.json"), append(beforeConfig, '\n'))
	if _, err = CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{
		LegacyDataDir: legacy, SnapshotDir: snapshot, PrimaryAccount: fixture.primary, ApplyDigest: preview.ApplyDigest,
	}); err == nil {
		t.Fatal("stale snapshot digest was accepted")
	}
	if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale apply published a target: %v", err)
	}
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "admin", "config.json"), beforeConfig)

	preview, err = CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{
		LegacyDataDir: legacy, SnapshotDir: snapshot, PrimaryAccount: fixture.primary,
	})
	if err != nil {
		t.Fatalf("fresh preview: %v", err)
	}
	created, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{
		LegacyDataDir: legacy, SnapshotDir: snapshot, PrimaryAccount: fixture.primary, ApplyDigest: preview.ApplyDigest,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if created.State != "created" || created.ApplyDigest != preview.ApplyDigest {
		t.Fatalf("unexpected created snapshot: %#v", created)
	}
	assertLegacyWechatSnapshotContents(t, snapshot, fixture)
	if got := mustReadSnapshotFixture(t, filepath.Join(snapshot, "bot-state", "messages-v2.json")); !bytes.Equal(got, fixture.primaryQueue) {
		t.Fatal("delivery queue changed while creating the import snapshot")
	}

	// Verify intentionally needs only the already-created snapshot.  It must
	// detect tampering without reopening, locking, or relying on the old tree.
	verified, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{SnapshotDir: snapshot, Verify: true})
	if err != nil || verified.State != "verified" {
		t.Fatalf("verify: %#v, %v", verified, err)
	}
	if _, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{
		LegacyDataDir: legacy, SnapshotDir: snapshot, PrimaryAccount: fixture.primary, ApplyDigest: preview.ApplyDigest,
	}); err == nil {
		t.Fatal("reapplying the preview overwrote the existing snapshot")
	}
	mustWriteSnapshotFixture(t, filepath.Join(snapshot, "unexpected.txt"), []byte("not declared by manifest"))
	if _, err = CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{SnapshotDir: snapshot, Verify: true}); err == nil {
		t.Fatal("verify accepted an unexpected snapshot file")
	}
	if err := os.Remove(filepath.Join(snapshot, "unexpected.txt")); err != nil {
		t.Fatal(err)
	}
	mustWriteSnapshotFixture(t, filepath.Join(snapshot, "admin", "config.json"), []byte(`{"enabled":false}`))
	if _, err = CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{SnapshotDir: snapshot, Verify: true}); err == nil {
		t.Fatal("verify accepted a tampered snapshot")
	}
}

func TestCaptureLegacyWechatSnapshotRejectsUnsafeSources(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the legacy desktop snapshot contract is Windows-only")
	}
	ctx := context.Background()
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy-v2")
	fixture := newLegacyWechatSnapshotFixture(t, legacy)

	for _, target := range []string{legacy, filepath.Join(legacy, "nested-snapshot")} {
		t.Run("overlap_"+filepath.Base(target), func(t *testing.T) {
			if _, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{LegacyDataDir: legacy, SnapshotDir: target, PrimaryAccount: fixture.primary}); err == nil {
				t.Fatal("source/target overlap was accepted")
			}
		})
	}

	t.Run("lock_held", func(t *testing.T) {
		unlock, err := hostfile.TryLock(filepath.Join(legacy, legacyCutoverLockFile))
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if _, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{LegacyDataDir: legacy, SnapshotDir: filepath.Join(base, "locked"), PrimaryAccount: fixture.primary}); err == nil {
			t.Fatal("snapshot proceeded while the legacy lease was held")
		}
	})

	t.Run("sqlite_wal", func(t *testing.T) {
		wal := filepath.Join(legacy, "admin", "companion", "memory.sqlite-wal")
		mustWriteSnapshotFixture(t, wal, []byte("synthetic WAL sidecar"))
		defer os.Remove(wal)
		if _, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{LegacyDataDir: legacy, SnapshotDir: filepath.Join(base, "wal"), PrimaryAccount: fixture.primary}); err == nil {
			t.Fatal("WAL-bearing legacy memory was accepted as a standalone snapshot")
		}
	})
	t.Run("corrupt_sqlite", func(t *testing.T) {
		memory := filepath.Join(legacy, "admin", "companion", legacyMemoryDB)
		raw := mustReadSnapshotFixture(t, memory)
		defer mustWriteSnapshotFixture(t, memory, raw)
		mustWriteSnapshotFixture(t, memory, []byte("SQLite format 3\x00corrupt synthetic file"))
		if _, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{LegacyDataDir: legacy, SnapshotDir: filepath.Join(base, "corrupt"), PrimaryAccount: fixture.primary}); err == nil {
			t.Fatal("corrupt SQLite memory was accepted")
		}
	})

	// A required file must never be followed through a reparse point.  Do not
	// make developer-mode symlink support a prerequisite for the rest of this
	// test.
	t.Run("required_file_link", func(t *testing.T) {
		config := filepath.Join(legacy, "admin", "config.json")
		raw := mustReadSnapshotFixture(t, config)
		outside := filepath.Join(base, "outside-config.json")
		mustWriteSnapshotFixture(t, outside, raw)
		if err := os.Remove(config); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, config); err != nil {
			mustWriteSnapshotFixture(t, config, raw)
			t.Skipf("symlink creation unavailable: %v", err)
		}
		defer func() {
			_ = os.Remove(config)
			mustWriteSnapshotFixture(t, config, raw)
		}()
		if _, err := CaptureLegacyWechatSnapshot(ctx, LegacyWechatSnapshotRequest{LegacyDataDir: legacy, SnapshotDir: filepath.Join(base, "link"), PrimaryAccount: fixture.primary}); err == nil {
			t.Fatal("legacy config reparse point was accepted")
		}
	})
}

type legacyWechatSnapshotFixture struct {
	primary      string
	secondary    string
	primaryQueue []byte
}

func newLegacyWechatSnapshotFixture(t *testing.T, legacy string) legacyWechatSnapshotFixture {
	t.Helper()
	const primary, secondary, dataset = "one", "two", "old-dataset"
	writeJSON := func(path string, value any) {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		mustWriteSnapshotFixture(t, path, b)
	}
	marker := legacyStopMarker{Version: 2, State: "stopped", Epoch: strings.Repeat("a", 32), AccountID: primary, DataRoot: legacy, NativeServerID: dataset, At: time.Now().UTC().Add(-time.Minute)}
	writeJSON(filepath.Join(legacy, legacyStopMarkerFile), marker)
	mustWriteSnapshotFixture(t, filepath.Join(legacy, legacyCutoverLockFile), []byte("synthetic stopped desktop lease"))

	// These are intentionally a mixture of permitted WeChat policy and secrets
	// from unrelated providers.  Snapshot code must canonicalize the former and
	// leave the latter behind.
	const policy = `{"enabled":true,"connection_paused":false,"api_key":"never-copy-me","base_url":"https://secret.example","private_enabled":true,"direct_ids":["friend-one"],"companion":{"enabled":true,"permissions":{"reply_text":true}},"group_rules":[{"id":"123@chatroom","mode":"mention","prefix":"/fish"}]}`
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "admin", "config.json"), []byte(policy))
	writeJSON(filepath.Join(legacy, "admin", "accounts.json"), []wechatapp.LegacyAccountRecord{{ID: secondary, Label: "secondary"}, {ID: "removed", Removed: true}})
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "admin", "accounts", wechatapp.AccountDirectory(secondary), "config.json"), []byte(policy))
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "admin", "api-token"), []byte("do-not-copy-admin-token"))
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "native", "api-token"), []byte("do-not-copy-native-token"))

	queue := func(account string) []byte {
		return []byte(`{"version":1,"server":"old-dataset","account":"` + account + `","deliveries":[{"key":"accepted-` + account + `","id":"job-` + account + `","thread":"123@chatroom","state":"accepted","server_id":"server-` + account + `","text":"private old body","input":{"account":"` + account + `","thread":"123@chatroom"}}]}`)
	}
	primaryQueue := queue(primary)
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "bot-state", "messages-v2.json"), primaryQueue)
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "bot-state", "checkpoint.json"), []byte(`{"version":1,"server_id":"old-dataset","account":"one","cursor":"10"}`))
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "bot-state", "accounts", wechatapp.AccountDirectory(secondary), "messages-v2.json"), queue(secondary))
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "bot-state", "accounts", wechatapp.AccountDirectory(secondary), "checkpoint.json"), []byte(`{"version":1,"server_id":"old-dataset","account":"two","cursor":"20"}`))

	for _, dir := range []string{
		filepath.Join(legacy, "admin", "companion"),
		filepath.Join(legacy, "admin", "accounts", wechatapp.AccountDirectory(secondary), "companion"),
	} {
		store, err := companion.NewStore(dir)
		if err != nil {
			t.Fatalf("create synthetic memory: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close synthetic memory: %v", err)
		}
	}
	return legacyWechatSnapshotFixture{primary: primary, secondary: secondary, primaryQueue: primaryQueue}
}

func assertLegacyWechatSnapshotContents(t *testing.T, snapshot string, fixture legacyWechatSnapshotFixture) {
	t.Helper()
	for _, relative := range []string{
		legacyStopMarkerFile,
		"legacy-wechat-snapshot.json",
		filepath.Join("admin", "config.json"),
		filepath.Join("admin", "accounts.json"),
		filepath.Join("admin", "companion", "memory.sqlite"),
		filepath.Join("bot-state", "messages-v2.json"),
		filepath.Join("bot-state", "checkpoint.json"),
		filepath.Join("admin", "accounts", wechatapp.AccountDirectory(fixture.secondary), "config.json"),
		filepath.Join("admin", "accounts", wechatapp.AccountDirectory(fixture.secondary), "companion", "memory.sqlite"),
		filepath.Join("bot-state", "accounts", wechatapp.AccountDirectory(fixture.secondary), "messages-v2.json"),
		filepath.Join("bot-state", "accounts", wechatapp.AccountDirectory(fixture.secondary), "checkpoint.json"),
	} {
		if info, err := os.Stat(filepath.Join(snapshot, relative)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("missing snapshot file %s: %v", relative, err)
		}
	}
	for _, relative := range []string{filepath.Join("admin", "api-token"), filepath.Join("native", "api-token")} {
		if _, err := os.Stat(filepath.Join(snapshot, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("secret copied into snapshot at %s: %v", relative, err)
		}
	}
	config := string(mustReadSnapshotFixture(t, filepath.Join(snapshot, "admin", "config.json")))
	for _, forbidden := range []string{"api_key", "base_url", "never-copy-me", "secret.example"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("filtered policy retained %q: %s", forbidden, config)
		}
	}
}

func mustWriteSnapshotFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func mustReadSnapshotFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
