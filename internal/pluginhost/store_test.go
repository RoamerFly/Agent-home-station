package pluginhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readyFixture(t *testing.T) string {
	t.Helper()
	src := "../../plugins/speech"
	dest := t.TempDir()
	if err := snapshot(context.Background(), src, dest); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dest, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p Package
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	p.Status = "ready"
	p.Runtime.Transport = "mcp-http"
	raw, _ = json.Marshal(p)
	if err = os.WriteFile(filepath.Join(dest, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dest
}
func TestPreparedSnapshotApprovalAndReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := OpenStore(dir, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := readyFixture(t)
	prepared, err := store.Prepare(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	// Changes after preparation cannot alter the bytes approved by the admin.
	os.WriteFile(filepath.Join(source, "README.md"), []byte("changed after preparation"), 0600)
	if _, err = store.Install(ctx, prepared.Token, "wrong", nil); err == nil {
		t.Fatal("wrong digest accepted")
	}
	if _, err = store.Install(ctx, prepared.Token, prepared.Digest, []string{"device"}); err == nil {
		t.Fatal("undeclared permission accepted")
	}
	installed, err := store.Install(ctx, prepared.Token, prepared.Digest, []string{"artifact.read"})
	if err != nil {
		t.Fatal(err)
	}
	if installed.Enabled || installed.Removed || installed.Revision != 1 {
		t.Fatal("unsafe initial state")
	}
	if _, err = store.Install(ctx, prepared.Token, prepared.Digest, nil); err == nil {
		t.Fatal("approval replay accepted")
	}
	store.Close()
	reopened, err := OpenStore(dir, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err := reopened.List(ctx)
	if err != nil || len(items) != 1 || items[0].Digest != prepared.Digest {
		t.Fatalf("reopen: %v %+v", err, items)
	}
	if wrong, err := OpenStore(dir, "account-b"); err == nil {
		wrong.Close()
		t.Fatal("cross-account store accepted")
	}
}
func TestPendingPackageAndPreparedTamperRejected(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err := store.Prepare(ctx, "../../plugins/speech")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Install(ctx, pending.Token, pending.Digest, nil); err == nil {
		t.Fatal("pending package installed")
	}
	p, err := store.Prepare(ctx, readyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := store.PackageDirectory(p.Digest)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed snapshot"), 0600)
	if _, err = store.Install(ctx, p.Token, p.Digest, nil); err == nil {
		t.Fatal("tampered snapshot installed")
	}
	items, _ := store.List(ctx)
	if len(items) != 0 {
		t.Fatal("failed installation left active registration")
	}
}
func TestPrepareCannotRecursivelyCopyOwnStore(t *testing.T) {
	parent := t.TempDir()
	s, err := OpenStore(filepath.Join(parent, "store"), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Prepare(context.Background(), parent); err == nil {
		t.Fatal("recursive source accepted")
	}
}
