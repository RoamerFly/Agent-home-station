package station

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/stationservices"
)

func memoryOnlyArchiveFixture(t *testing.T) (*surfShareFixture, stationMemoryArchiveInput) {
	t.Helper()
	x := newSurfShareFixture(t)
	x.request("PUT", x.path+"/policy", map[string]any{"reply_threads": []string{"target@chatroom"}, "history_threads": []string{"target@chatroom"}}, 200)
	dir := t.TempDir()
	store, err := companion.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := companion.Defaults()
	cfg.Enabled, cfg.Permissions.Remember = true, true
	now := time.Now().UTC()
	for _, thread := range []string{"target@chatroom", "second@chatroom"} {
		in := companion.Input{Channel: "wechat", Account: "old-alias", Thread: thread, Sender: "synthetic-sender", TraceID: "synthetic-" + thread, Text: "合成来源原话", MessageTime: now.Format(time.RFC3339Nano), Source: &companion.SourceRef{Integrity: "observed", MessageType: 1, ServerID: "synthetic-original-" + thread}}
		if err := store.Observe(in, cfg, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return x, stationMemoryArchiveInput{Database: filepath.Join(dir, "memory.sqlite"), Account: "old-alias"}
}

func TestWechatMemoryOnlyArchiveAliasPreviewApplyAndRead(t *testing.T) {
	x, in := memoryOnlyArchiveFixture(t)
	source, _ := os.ReadFile(in.Database)
	before := sha256.Sum256(source)
	route := x.path + "/memory-archive"
	if code, _ := x.f.request("POST", route, in, false); code != 403 {
		t.Fatalf("missing csrf = %d", code)
	}
	preview := x.request("POST", route, in, 200)
	if preview["source_account"] != "old-alias" || preview["target_account"] != "one" || len(preview["conversations"].([]any)) != 2 {
		t.Fatal(preview)
	}
	in.Thread = "second@chatroom"
	x.request("POST", route, in, 409)
	in.Thread = "target@chatroom"
	ready := x.request("POST", route, in, 200)
	if ready["status"] != "ready" {
		t.Fatal(ready)
	}
	in.Digest = ready["digest"].(string)
	x.request("POST", route, in, 400)
	in.SameAccount = true
	applied := x.request("POST", route, in, 200)
	if applied["applied"] != true {
		t.Fatal(applied)
	}
	x.request("POST", route, in, 200)
	item, err := readStationLegacyMemory(x.worker.dir, "one", in.Thread, x.worker.policy.HistoryGrants[in.Thread])
	if err != nil || item.SourceAccount != "old-alias" || item.SourceSHA256 != ready["source_sha256"] || len(item.Selection.Events) != 1 {
		t.Fatal("archive provenance", item, err)
	}
	// Read through the actual authorized archive endpoint, never the live DB.
	history := x.request("GET", x.path+"/conversations/"+in.Thread+"/imported-memory?kind=events", nil, 200)
	if history["returned_count"] != float64(1) {
		t.Fatal(history)
	}
	ledger, err := companion.NewStore(filepath.Join(x.worker.dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.ScopeIdentities()) != 0 {
		t.Fatal("archive copied originals into live context")
	}
	ledger.Close()
	source, _ = os.ReadFile(in.Database)
	if sha256.Sum256(source) != before || len(x.sends) != 0 {
		t.Fatal("source modified or native send executed")
	}
	// The saved alias binding cannot follow a replacement native dataset.
	x.mu.Lock()
	x.dataset = "different-synthetic-dataset"
	x.mu.Unlock()
	x.reconnect()
	x.request("GET", x.path+"/conversations/"+in.Thread+"/imported-memory?kind=events", nil, 409)
	in.Digest = string(make([]byte, 64))
	if _, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true }); err == nil {
		t.Fatal("idempotent archive ignored mismatched digest")
	}
}

func TestWechatMemoryOnlyArchiveForgottenScopeAndRunningWorker(t *testing.T) {
	x, in := memoryOnlyArchiveFixture(t)
	in.Thread = "target@chatroom"
	ready, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	in.Digest, in.SameAccount = ready.Digest, true
	ledger, err := companion.NewStore(filepath.Join(x.worker.dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	cfg := companion.Defaults()
	cfg.Permissions.Remember = true
	if err := ledger.Observe(companion.Input{Channel: "wechat", Account: "one", Thread: in.Thread, Sender: "synthetic-current", TraceID: "current-forget-source", Text: "合成当前消息"}, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Clear(companion.ScopeID("wechat", "one", in.Thread)); err != nil {
		t.Fatal(err)
	}
	ledger.Close()
	if _, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true }); err == nil {
		t.Fatal("forget barrier ignored")
	}
	x.worker.mu.Lock()
	x.worker.policy.Enabled = true
	x.worker.policy.Paused = false
	x.worker.mu.Unlock()
	in.Thread, in.Digest = "", ""
	if _, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true }); err == nil {
		t.Fatal("running connection accepted migration")
	}
	if _, err := os.Stat(filepath.Join(x.worker.dir, stationLegacyMemoryFile("one", "target@chatroom"))); !os.IsNotExist(err) {
		t.Fatal("rejected migration created archive", err)
	}
}

func TestWechatMemoryOnlyArchiveRejectsChangedSourceGrantAndDataset(t *testing.T) {
	x, in := memoryOnlyArchiveFixture(t)
	in.Thread = "target@chatroom"
	ready, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	in.Digest, in.SameAccount = ready.Digest, true
	x.worker.mu.Lock()
	x.worker.policy.HistoryGrants[in.Thread] = time.Now().Add(time.Second).UTC()
	x.worker.mu.Unlock()
	if _, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true }); err == nil {
		t.Fatal("regranted scope accepted stale preview")
	}
	in.Digest = ""
	ready, err = x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	in.Digest = ready.Digest
	x.mu.Lock()
	x.dataset = "replacement-native-dataset"
	x.mu.Unlock()
	if _, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true }); err == nil {
		t.Fatal("replacement dataset accepted preview")
	}
	x.reconnect()
	in.Digest = ""
	ready, err = x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	in.Digest = ready.Digest
	f, err := os.OpenFile(in.Database, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("changed"))
	f.Close()
	if _, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true }); err == nil {
		t.Fatal("changed database accepted old digest")
	}
	if _, err := os.Stat(filepath.Join(x.worker.dir, stationLegacyMemoryFile("one", in.Thread))); !os.IsNotExist(err) {
		t.Fatal("rejected archive published", err)
	}
}

func TestWechatMemoryOnlyArchiveSourceJournalAndCancellation(t *testing.T) {
	x, in := memoryOnlyArchiveFixture(t)
	if err := os.WriteFile(in.Database+"-wal", []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStationMemoryArchive(t.Context(), in.Database); err == nil {
		t.Fatal("pending WAL accepted")
	}
	problem := x.request("POST", x.path+"/memory-archive", in, 409)
	if !strings.Contains(problem["error"].(string), "不要删除日志文件") {
		t.Fatal("journal remediation missing", problem)
	}
	os.Remove(in.Database + "-wal")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if a, err := openStationMemoryArchive(ctx, in.Database); err == nil {
		a.close()
		t.Fatal("cancelled read succeeded")
	}
	if _, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return false }); err == nil {
		t.Fatal("stale owner admitted")
	}
}

func TestWechatMemoryOnlyArchiveOwnerSelectedSourceWithinWorkspace(t *testing.T) {
	x, in := memoryOnlyArchiveFixture(t)
	raw, err := os.ReadFile(in.Database)
	if err != nil {
		t.Fatal(err)
	}
	in.Database = filepath.Join(x.host.workspaceRoot, "old-memory.sqlite")
	if err := os.WriteFile(in.Database, raw, 0600); err != nil {
		t.Fatal(err)
	}
	view, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true })
	if err != nil || len(view.Conversations) != 2 {
		t.Fatal("owner-selected readonly source incorrectly restricted by workspace location", view, err)
	}
}

func TestWechatMemoryOnlyArchiveAutomaticallySelectsSoleSourceAccount(t *testing.T) {
	x, in := memoryOnlyArchiveFixture(t)
	in.Account = ""
	view := x.request("POST", x.path+"/memory-archive", in, 200)
	if view["source_account"] != "old-alias" || len(view["conversations"].([]any)) != 2 || view["applied"] != false {
		t.Fatal("single account autodiscovery", view)
	}
	in.Thread = "target@chatroom"
	ready := x.request("POST", x.path+"/memory-archive", in, 200)
	in.Digest = ready["digest"].(string)
	in.SameAccount = true
	x.request("POST", x.path+"/memory-archive", in, 400)
	in.Account = ready["source_account"].(string)
	x.request("POST", x.path+"/memory-archive", in, 200)
}

func TestWechatMemoryOnlyArchiveMultipleSourceAccountsRequireSelection(t *testing.T) {
	x, in := memoryOnlyArchiveFixture(t)
	store, err := companion.NewStore(filepath.Dir(in.Database))
	if err != nil {
		t.Fatal(err)
	}
	cfg := companion.Defaults()
	cfg.Permissions.Remember = true
	input := companion.Input{Channel: "wechat", Account: "second-old-alias", Thread: "private-foreign-thread", Sender: "synthetic-foreign", TraceID: "synthetic-foreign-message", Text: "合成其他账号内容", Source: &companion.SourceRef{Integrity: "observed", MessageType: 1}}
	if err := store.Observe(input, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	store.Close()
	in.Account = ""
	view := x.request("POST", x.path+"/memory-archive", in, 200)
	if view["status"] != "choose_account" || len(view["source_accounts"].([]any)) != 2 || len(view["conversations"].([]any)) != 0 || view["digest"] != nil {
		t.Fatal("ambiguous account auto-selected", view)
	}
	in.Account = "old-alias"
	view = x.request("POST", x.path+"/memory-archive", in, 200)
	if len(view["conversations"].([]any)) != 2 {
		t.Fatal("selected scope widened", view)
	}
}

func TestWechatMemoryOnlyArchiveReadBindingOwnerAndDataset(t *testing.T) {
	x, in := memoryOnlyArchiveFixture(t)
	in.Thread = "target@chatroom"
	ready, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	in.Digest, in.SameAccount = ready.Digest, true
	if _, err := x.host.importMemoryArchive(t.Context(), x.id, in, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	item, err := readStationLegacyMemory(x.worker.dir, "one", in.Thread, x.worker.policy.HistoryGrants[in.Thread])
	if err != nil {
		t.Fatal(err)
	}
	native, err := x.host.WorkerSender(t.Context(), x.id)
	if err != nil {
		t.Fatal(err)
	}
	if !x.host.mappedArchiveAllowed(t.Context(), native, item) {
		t.Fatal("original mapping rejected")
	}
	original := x.host.ownerReader
	defer func() { x.host.ownerReader = original }()
	person, err := original()
	if err != nil {
		t.Fatal(err)
	}
	person.ID = "different-synthetic-owner"
	x.host.ownerReader = func() (stationservices.Owner, error) { return person, nil }
	if x.host.mappedArchiveAllowed(t.Context(), native, item) {
		t.Fatal("new owner inherited mapped archive")
	}
	x.host.ownerReader = original
	if !x.host.mappedArchiveAllowed(t.Context(), native, item) {
		t.Fatal("original owner did not recover original mapping")
	}
	x.mu.Lock()
	x.dataset = "foreign-synthetic-dataset"
	x.mu.Unlock()
	if x.host.mappedArchiveAllowed(t.Context(), native, item) {
		t.Fatal("foreign dataset inherited mapped archive")
	}
}
