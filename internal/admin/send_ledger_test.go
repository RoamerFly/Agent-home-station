package admin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
)

func syntheticReceipt(i int) toolRecord {
	return toolRecord{Key: fmt.Sprintf("synthetic-%04d", i), Hash: "synthetic-content-hash", Account: "test", Thread: "filehelper", Kind: "voice", Created: time.Unix(int64(i+1), 0).UTC(), Status: linuxnative.ToolStatus{ID: fmt.Sprintf("job-%04d", i), State: "accepted", ServerID: fmt.Sprintf("%d", 123450000+i), ThreadID: "filehelper"}}
}

func fullSendStore(t *testing.T) *toolStore {
	t.Helper()
	s, err := newToolStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 1000 {
		r := syntheticReceipt(i)
		s.data.Sends[r.Key] = r
	}
	raw, _ := json.Marshal(s.data)
	if err := writePrivate(s.path, raw); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSendLedgerRotationRestartAndNoReplay(t *testing.T) {
	s := fullSendStore(t)
	// These must never be evicted, even though they are the oldest records.
	states := []string{"unknown", "running", "failed", "queued", "accepted"}
	for i, state := range states {
		r := s.data.Sends[syntheticReceipt(i).Key]
		r.Status.State, r.Status.ServerID = state, ""
		s.data.Sends[r.Key] = r
	}
	latest := syntheticReceipt(1000)
	_, created, err := s.claim(latest)
	if err != nil || !created || len(s.data.Sends) != 873 {
		t.Fatal(created, err, len(s.data.Sends))
	}
	for i := range states {
		if _, ok := s.data.Sends[syntheticReceipt(i).Key]; !ok {
			t.Fatal("unfinished or unverified receipt evicted")
		}
	}
	restarted, err := newToolStore(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	old := syntheticReceipt(5)
	got, created, err := restarted.claim(old)
	if err != nil || created || !got.archived || got.Status.ServerID != old.Status.ServerID {
		t.Fatal("archived request replayed", got, created, err)
	}
	got, ok, err := restarted.byID(old.Status.ID, old.Account)
	if err != nil || !ok || !got.archived || got.Key != old.Key {
		t.Fatal("job lookup lost", got, ok)
	}
	if _, ok, err = restarted.byID(old.Status.ID, "different-account"); ok || err != nil {
		t.Fatal("cross-account receipt exposed")
	}
	if _, ok, err = restarted.record(old.Key, "different-account"); ok || err != nil {
		t.Fatal("cross-account key exposed")
	}
	for _, altered := range []toolRecord{func() toolRecord { r := old; r.Hash = "changed"; return r }(), func() toolRecord { r := old; r.Account = "other"; return r }()} {
		if _, created, err := restarted.claim(altered); err == nil || created {
			t.Fatal("conflicting archived key accepted")
		}
	}
	if err := restarted.update(old.Key, old.Status); err != nil {
		t.Fatal("idempotent receipt refresh failed", err)
	}
	regressed := old.Status
	regressed.State = "unknown"
	if err := restarted.update(old.Key, regressed); err == nil {
		t.Fatal("immutable accepted receipt regressed")
	}
}

func TestSendLedgerCapacityCannotEvictUncertainRecords(t *testing.T) {
	s := fullSendStore(t)
	for key, r := range s.data.Sends {
		r.Status.State = "unknown"
		s.data.Sends[key] = r
	}
	if _, created, err := s.claim(syntheticReceipt(1000)); err == nil || created || len(s.data.Sends) != 1000 {
		t.Fatal("uncertain receipt discarded", created, err)
	}
	broken := toolData{Sends: map[string]toolRecord{"mismatched-key": syntheticReceipt(1)}}
	if err := s.compactSends(&broken, true); err == nil || len(broken.Sends) != 1 {
		t.Fatal("malformed identity discarded or would prevent compaction progress")
	}
}

func TestSendLedgerCompactsOnBytePressureBelowCountLimit(t *testing.T) {
	s, _ := newToolStore(t.TempDir())
	for i := range 850 {
		r := syntheticReceipt(i)
		r.Status.Reason = strings.Repeat("s", 1500)
		s.data.Sends[r.Key] = r
	}
	raw, _ := json.Marshal(s.data)
	if len(raw) >= 2<<20 {
		t.Fatal("invalid test setup")
	}
	if err := writePrivate(s.path, raw); err != nil {
		t.Fatal(err)
	}
	r := syntheticReceipt(849)
	r.Status.Reason = strings.Repeat("s", 900000)
	if err := s.update(r.Key, r.Status); err != nil {
		t.Fatal(err)
	}
	if len(s.data.Sends) >= 850 {
		t.Fatal("byte limit did not rotate accepted receipts")
	}
	if _, err := newToolStore(filepath.Dir(s.path)); err != nil {
		t.Fatal("compacted state cannot restart", err)
	}
	if got, exists, err := s.record(syntheticReceipt(0).Key, "test"); err != nil || !exists || !got.archived {
		t.Fatal("byte-pressure rotation lost original key", err)
	}
}

func TestSendLedgerFailureKeepsActiveState(t *testing.T) {
	for _, stage := range []string{"archive", "commit"} {
		t.Run(stage, func(t *testing.T) {
			s := fullSendStore(t)
			before, _ := os.ReadFile(s.path)
			if stage == "archive" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(s.path), "send-ledger"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(s.path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(s.path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := s.claim(syntheticReceipt(1000)); err == nil || len(s.data.Sends) != 1000 {
				t.Fatal("failed commit changed active state", err, len(s.data.Sends))
			}
			if stage == "commit" {
				os.Remove(s.path)
				os.WriteFile(s.path, before, 0600)
				// Retry can reuse the verified cold copies without duplication.
				if _, _, err := s.claim(syntheticReceipt(1000)); err != nil {
					t.Fatal(err)
				}
			} else {
				after, _ := os.ReadFile(s.path)
				if !bytes.Equal(before, after) {
					t.Fatal("archive failure overwrote original state")
				}
			}
		})
	}
}

func TestSendLedgerCorruptionAndMisroutingFailClosed(t *testing.T) {
	for _, damage := range []string{"gzip", "digest", "identity"} {
		t.Run(damage, func(t *testing.T) {
			s, _ := newToolStore(t.TempDir())
			r := syntheticReceipt(1)
			if err := s.archiveSend(r); err != nil {
				t.Fatal(err)
			}
			path, _ := s.ledgerPath("requests", r.Key, false)
			bad := []byte("corrupt")
			if damage != "gzip" {
				var payload any = archivedSend{Version: 1, Digest: strings.Repeat("0", 64), Record: r}
				if damage == "identity" {
					r.Key = "wrong-key"
					payload = archivedSend{Version: 1, Record: r}
				}
				raw, _ := json.Marshal(payload)
				var compressed bytes.Buffer
				z := gzip.NewWriter(&compressed)
				z.Write(raw)
				z.Close()
				bad = compressed.Bytes()
			}
			if err := os.WriteFile(path, bad, 0600); err != nil {
				t.Fatal(err)
			}
			if _, created, err := s.claim(syntheticReceipt(1)); err == nil || created || len(s.data.Sends) != 0 {
				t.Fatal("unreadable evidence permitted a resend", created, err)
			}
		})
	}
}

func TestSendLedgerStatusQueryUsesHistoricalReceipt(t *testing.T) {
	f := setup(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	f.login()
	r := syntheticReceipt(42)
	input := linuxnative.ToolSend{ThreadID: r.Thread, Kind: "text", Text: "synthetic archived message", IdempotencyKey: r.Key}
	raw, _ := json.Marshal(input)
	h := sha256.Sum256(raw)
	r.Hash, r.Kind, r.Status.Kind = hex.EncodeToString(h[:]), "text", "text"
	if err := f.manager.tools.archiveSend(r); err != nil {
		t.Fatal(err)
	}
	code, b, _ := f.request("GET", "/api/storage", nil, false, false)
	if code != 200 || !bytes.Contains(b, []byte(`"name":"历史发送回执索引"`)) {
		t.Fatal("receipt storage invisible", code, string(b))
	}
	restarted, err := newToolStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.tools = restarted
	// The fake native has no historical record. Both routes still return the
	// preserved acceptance and neither creates another send.
	for _, route := range []string{"/api/tools/sends/" + r.Status.ID, "/api/tools/lookup?key=" + r.Key} {
		code, b, _ := f.request("GET", route, nil, false, false)
		if code != 200 || !bytes.Contains(b, []byte(r.Status.ServerID)) {
			t.Fatal(route, code, string(b))
		}
	}
	for range 2 {
		code, b, _ := f.request("POST", "/api/tools/send", input, true, true)
		if code != 202 || !bytes.Contains(b, []byte(r.Status.ServerID)) {
			t.Fatal("historical submission replayed", code, string(b))
		}
	}
	if len(f.manager.tools.data.Sends) != 0 || native.submits.Load() != 0 {
		t.Fatal("historical status query changed active records")
	}
	path, _ := restarted.ledgerPath("requests", r.Key, false)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("GET", "/api/tools/lookup?key="+r.Key, nil, false, false); code != 503 {
		t.Fatal("damaged evidence mistaken for missing record", code)
	}
	if code, _, _ := f.request("POST", "/api/tools/send", input, true, true); code != 409 || native.submits.Load() != 0 {
		t.Fatal("damaged evidence permitted send", code)
	}
}

func TestSendLedgerRejectsSymlinksAndConflictingJobIDs(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		s, _ := newToolStore(t.TempDir())
		r := syntheticReceipt(0)
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(filepath.Dir(s.path), "send-ledger")); err != nil {
			t.Skip("symlinks unavailable on this host", err)
		}
		if _, _, err := s.claim(r); err == nil {
			t.Fatal("symlink accepted as a safe receipt store")
		}
		files, _ := os.ReadDir(target)
		if len(files) != 0 {
			t.Fatal("wrote outside private store")
		}
	})
	t.Run("job-id-conflict", func(t *testing.T) {
		s, _ := newToolStore(t.TempDir())
		first, second := syntheticReceipt(1), syntheticReceipt(2)
		second.Status.ID = first.Status.ID
		if err := s.archiveSend(first); err != nil {
			t.Fatal(err)
		}
		if err := s.archiveSend(second); err == nil {
			t.Fatal("conflicting job identity overwritten")
		}
		got, ok, err := s.byID(first.Status.ID, first.Account)
		if err != nil || !ok || got.Key != first.Key {
			t.Fatal("original evidence lost", got, err)
		}
	})
}
