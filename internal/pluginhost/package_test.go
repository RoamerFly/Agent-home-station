package pluginhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDependencyConstraintContract(t *testing.T) {
	for _, tc := range []struct {
		requirement, version string
		want                 bool
	}{
		{"", "opaque-legacy", true}, {"^1.2.0", "1.9.3", true}, {"^1.2.0", "2.0.0", false},
		{"~1.2.0", "1.2.8", true}, {"~1.2.0", "1.3.0", false}, {">=1.2.0 <2.0.0", "1.2.0", true},
		{">=1.2.0 <2.0.0", "2.0.0", false}, {"1.2", "1.2.0", false}, {"^0.1.1", "0.1.9", true},
		{"^0.1.1", "0.2.0", false}, {"^0.0.1", "0.0.2", false}, {"^0.0.1", "0.0.1", true},
		{"^1.2.3", "+1.2.3", false}, {" ", "1.2.3", false},
	} {
		if got := DependencyCompatible(tc.requirement, tc.version); got != tc.want {
			t.Fatalf("%q against %q = %v", tc.requirement, tc.version, got)
		}
	}
	var legacy Package
	if err := json.Unmarshal([]byte(`{"dependencies":["media-understanding"]}`), &legacy); err != nil || len(legacy.Dependencies) != 1 || legacy.Dependencies[0].ID != "media-understanding" {
		t.Fatal(err, legacy.Dependencies)
	}
	var fixed Package
	if err := json.Unmarshal([]byte(`{"dependencies":[{"id":"media-understanding","version":"^1.2.0"}]}`), &fixed); err != nil || fixed.Dependencies[0].Version != "^1.2.0" {
		t.Fatal(err, fixed.Dependencies)
	}
	if validDependencyConstraint("1.2") || validDependencyConstraint(" ") || !validDependencyConstraint("^1.2.0") {
		t.Fatal("constraint validation")
	}
	for _, raw := range []string{`{"dependencies":[{"id":"media-understanding","version":"^1.2.0","surprise":true}]}`, `{"dependencies":[{"id":"media-understanding","version":"^1.2.0"},"legacy"]}`} {
		var p Package
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			t.Fatal("ambiguous dependency declaration accepted", raw)
		}
	}
}

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
			if d.Name() == "web-search" || d.Name() == "media-understanding" || d.Name() == "speech" || d.Name() == "music" || d.Name() == "share-reader" || d.Name() == "mobile-control" || d.Name() == "project-development" {
				if p.Status != "ready" || p.Runtime.Transport != "mcp-stdio" || p.Runtime.Entrypoint != "runtime/main.py" {
					t.Fatal("web runtime not connected")
				}
				if d.Name() != "web-search" {
					want := map[string]bool{"media.observe": true, "media.transcribe": true, "media.listen": true}
					if d.Name() == "speech" {
						want = map[string]bool{"speech.synthesize": true, "speech.synthesize_named": true}
					}
					if d.Name() == "music" {
						want = map[string]bool{"music.generate": true, "music.bridge_request": true, "music.service_control": true}
						if len(p.Permissions) != 2 || p.Permissions[0] != "model.invoke" || p.Permissions[1] != "network" {
							t.Fatal("music package must declare separate cloud and local bridge proxies")
						}
					}
					if d.Name() == "share-reader" {
						want = map[string]bool{"share.resolve_public": true, "share.resolve_channels": true}
					}
					if d.Name() == "project-development" {
						want = map[string]bool{"project.workspace_read": true, "project.workspace_write": true, "project.workspace_execute": true}
						if len(p.Permissions) != 3 || p.Permissions[0] != "workspace.read" || p.Permissions[1] != "workspace.write" || p.Permissions[2] != "process.execute" {
							t.Fatal("project package must declare three separate workspace/process proxies")
						}
					}
					if d.Name() == "mobile-control" {
						want = map[string]bool{"device.plan": true, "device.list": true, "device.observe": true, "device.act": true, "device.record": true, "device.read_artifact": true}
						if len(p.Permissions) != 2 || p.Permissions[0] != "model.invoke" || p.Permissions[1] != "device" {
							t.Fatal("device package must declare only separate model/device proxies")
						}
					}
					raw, err := os.ReadFile(filepath.Join("../../plugins", d.Name(), "capabilities.json"))
					if err != nil {
						t.Fatal(err)
					}
					var doc struct {
						Capabilities []Capability `json:"capabilities"`
					}
					if err = json.Unmarshal(raw, &doc); err != nil {
						t.Fatal(err)
					}
					for _, cap := range doc.Capabilities {
						if want[cap.ID] {
							if cap.Implementation != "ready" || !cap.Discoverable {
								t.Fatal("implemented capability hidden", cap.ID)
							}
							if d.Name() == "mobile-control" {
								permission := "device"
								if cap.ID == "device.plan" {
									permission = "model.invoke"
								}
								if cap.Async || len(cap.Permissions) != 1 || cap.Permissions[0] != permission {
									t.Fatal("device capability must remain synchronous and use only its own proxy")
								}
							}
							if d.Name() == "music" {
								permission := "model.invoke"
								if cap.ID != "music.generate" {
									permission = "network"
								}
								if cap.Async || len(cap.Permissions) != 1 || cap.Permissions[0] != permission {
									t.Fatal("music capability must remain synchronous and use its own proxy")
								}
							}
							if d.Name() == "project-development" {
								permission := map[string]string{
									"project.workspace_read":    "workspace.read",
									"project.workspace_write":   "workspace.write",
									"project.workspace_execute": "process.execute",
								}[cap.ID]
								if cap.Async || len(cap.Permissions) != 1 || cap.Permissions[0] != permission {
									t.Fatal("project capability must use only its corresponding host proxy", cap.ID)
								}
							}
							if d.Name() == "speech" {
								wantPermissions := []string{"model.invoke"}
								if cap.ID == "speech.synthesize_named" {
									wantPermissions = []string{"artifact.read", "model.invoke"}
								}
								if cap.Async || len(cap.Permissions) != len(wantPermissions) {
									t.Fatal("speech capability must remain synchronous with exact proxy permissions", cap.ID)
								}
								for i, permission := range wantPermissions {
									if cap.Permissions[i] != permission {
										t.Fatal("speech capability permission changed", cap.ID, cap.Permissions)
									}
								}
							}
							if d.Name() == "media-understanding" && (cap.Async || len(cap.Permissions) != 2 || cap.Permissions[0] != "artifact.read" || cap.Permissions[1] != "model.invoke") {
								t.Fatal("media capability must remain synchronous and use only artifact/model proxies", cap.ID)
							}
							delete(want, cap.ID)
						} else if cap.Implementation != "migration-pending" || cap.Discoverable {
							t.Fatal("future capability exposed", cap.ID)
						}
					}
					if len(want) != 0 {
						t.Fatal("missing implemented capability", want)
					}
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
