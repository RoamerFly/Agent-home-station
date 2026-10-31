package pluginhost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOfficialContracts(t *testing.T) {
	dirs, err := os.ReadDir("../../plugins")
	if err != nil {
		t.Fatal(err)
	}
	folders := dirs[:0]
	for _, d := range dirs {
		if d.IsDir() {
			folders = append(folders, d)
		}
	}
	dirs = folders
	if len(dirs) != 9 {
		t.Fatalf("expected 9 capability packages, got %d", len(dirs))
	}
	for _, d := range dirs {
		t.Run(d.Name(), func(t *testing.T) {
			p, e := Verify(filepath.Join("../../plugins", d.Name()))
			if e != nil {
				t.Fatal(e)
			}
			if d.Name() == "web-search" {
				if p.Status != "ready" || p.Runtime.Transport != "mcp-stdio" || p.Runtime.Entrypoint != "runtime/main.py" {
					t.Fatal("web runtime not connected")
				}
			} else if p.Status != "migration-pending" {
				t.Fatal("unimplemented capability claimed ready")
			}
		})
	}
}
func TestPackageRejectsTampering(t *testing.T) {
	src := "../../plugins/speech"
	dir := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		return os.WriteFile(target, b, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(dir); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0600)
	if _, err = Verify(dir); err == nil {
		t.Fatal("modified file accepted")
	}
}
func TestPermissionScopeAndRecovery(t *testing.T) {
	p := Package{ID: "speech", Version: "1", Status: "ready", Permissions: []string{"artifact.read"}}
	c := Invocation{Account: "a", Thread: "t", Requester: "u", Purpose: "synthetic", RequestKey: "r"}
	g := Grant{Account: "a", Plugin: "speech", Version: "1", Permissions: []string{"artifact.read"}}
	if e := Authorize(p, g, c, []string{"artifact.read"}); e != nil {
		t.Fatal(e)
	}
	g.Account = "b"
	if Authorize(p, g, c, nil) == nil {
		t.Fatal("cross account accepted")
	}
	g.Account = "a"
	if Authorize(p, g, c, []string{"network"}) == nil {
		t.Fatal("permission escalation")
	}
	p.Status = "migration-pending"
	if Authorize(p, g, c, nil) == nil {
		t.Fatal("pending implementation executed")
	}
	if (Job{State: "running"}).Recovery() != "needs_review" || (Job{State: "running", RemoteID: "remote"}).Recovery() != "query" {
		t.Fatal("unsafe recovery")
	}
}
