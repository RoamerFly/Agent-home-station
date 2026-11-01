package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func requireRepairSandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("fixture grading uses the isolated Linux runtime; no host Python fallback")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("Bubblewrap required")
	}
}

func TestRepairLegacyWorkspaceAndRecordIdentityFailClosed(t *testing.T) {
	f, _ := trustedAgent(t)
	f.manager.config.Companion.Enabled = true
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	original := []byte("synthetic host file must remain untouched")
	outside := filepath.Join(t.TempDir(), "host.txt")
	if err := os.WriteFile(outside, original, 0600); err != nil {
		t.Fatal(err)
	}
	v := repairTestRun{ID: strings.Repeat("a", 64), Recipient: "wxid_owner", Project: filepath.Dir(outside), Stage: 1}
	if err := f.manager.saveRepairRun(v); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.openRepairProject(v); err == nil {
		t.Fatal("legacy host path admitted")
	}
	f.login()
	if code, _, _ := f.request("GET", "/api/agent/repair-tests/"+v.ID, nil, true, false); code != 400 {
		t.Fatal("legacy host evaluation admitted", code)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != string(original) {
		t.Fatal("outside file changed", err)
	}
	body, _ := json.Marshal(v)
	wrong := strings.Repeat("b", 64)
	if err := os.WriteFile(f.manager.repairRunPath(wrong), body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.loadRepairRun(wrong); err == nil {
		t.Fatal("record identity mismatch admitted")
	}
}

func TestRepairUnavailableExecutorDoesNotScheduleOrClaimReproducedBug(t *testing.T) {
	f, _ := trustedAgent(t)
	f.manager.config.Companion.Enabled = true
	settings := f.manager.agent.config()
	settings.Host.Mode = "full"
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	v, err := f.manager.startRepairRun(t.Context(), "wxid_owner")
	if err == nil || !strings.Contains(err.Error(), "sandbox_unavailable") || v.BaselineFailed {
		t.Fatal("missing isolation counted as a reproduced bug", v.BaselineFailed, err)
	}
	if len(f.manager.agent.list("test", "wxid_owner")) != 0 {
		t.Fatal("unexecuted fixture scheduled a repair")
	}
}

func TestRepairFixtureRootRejectsLinksAndConflictingFiles(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = createRepairFixture(root, "fixture.py", "synthetic fixture"); err != nil {
		t.Fatal(err)
	}
	if err = createRepairFixture(root, "fixture.py", "synthetic fixture"); err != nil {
		t.Fatal("same fixture not reusable", err)
	}
	if err = createRepairFixture(root, "fixture.py", "different body"); err == nil {
		t.Fatal("fixture silently overwritten")
	}
	if err = createRepairFixture(root, "../escape.py", "denied"); err == nil {
		t.Fatal("fixture escaped root")
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err = os.WriteFile(outside, []byte("outside synthetic data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir, "link.py")); err != nil {
		t.Skip("symlink unavailable on this platform")
	}
	if repairFileHash(root, "link.py") != "" {
		t.Fatal("controller followed external file link")
	}
	if err = createRepairFixture(root, "link.py", "new content"); err == nil {
		t.Fatal("controller wrote through external link")
	}
}

func TestRepairFixtureAndIndependentGraderUseSystemIsolation(t *testing.T) {
	requireRepairSandbox(t)
	_, p := managedAccountPair(t)
	m := p[0].manager
	m.mu.Lock()
	m.config.Companion.Enabled = true
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	v, err := m.startRepairRun(ctx, "wxid_owner")
	if err != nil || !v.BaselineFailed {
		t.Fatal("isolated faulty fixture did not run", err)
	}
	outside := filepath.Join(t.TempDir(), "host-secret.txt")
	if err = os.WriteFile(outside, []byte("synthetic host canary"), 0600); err != nil {
		t.Fatal(err)
	}
	forbidden, _ := json.Marshal([]string{outside, p[1].executionContext().Private, filepath.Join(m.dir, "agent-config.json")})
	probe := `import pathlib,json,subprocess,sys
_paths=json.loads(FORBIDDEN)
_visible=any(pathlib.Path(p).exists() for p in _paths)
_child=subprocess.run([sys.executable,'-I','-c','import pathlib,sys,json;sys.exit(9 if any(pathlib.Path(p).exists() for p in json.loads(sys.argv[1])) else 0)',json.dumps(_paths)],capture_output=True)
pathlib.Path('sandbox-probe.json').write_text(json.dumps({'host_visible':_visible,'child_exit':_child.returncode}))
`
	quoted, _ := json.Marshal(string(forbidden))
	probe = strings.ReplaceAll(probe, "FORBIDDEN", string(quoted))
	if err = os.WriteFile(filepath.Join(v.Project, "shipping.py"), []byte(probe+repairSource), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"tests", "1"} {
		code, _, err := m.runRepairProgram(ctx, v, mode)
		if err != nil || code != 1 {
			t.Fatal("faulty fixture/import not executed in sandbox", mode, code, err)
		}
		data, err := os.ReadFile(filepath.Join(v.Project, "sandbox-probe.json"))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Visible   bool `json:"host_visible"`
			ChildExit int  `json:"child_exit"`
		}
		if json.Unmarshal(data, &result) != nil || result.Visible || result.ChildExit != 0 {
			t.Fatal("controller evaluator exposed host/other-account data", string(data))
		}
	}
	// Revocation stops an already running grader, not just a future call.
	waiting := "import pathlib,time\npathlib.Path('revocation-ready').touch()\ntime.sleep(10)\npathlib.Path('after-revocation').touch()\n"
	if err = os.WriteFile(filepath.Join(v.Project, "shipping.py"), []byte(waiting+repairSource), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := m.runRepairProgram(ctx, v, "tests"); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(v.Project, "revocation-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated evaluator did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cfg := m.agent.config()
	cfg.OwnerIDs = []string{"different-owner"}
	if err = m.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("running evaluator ignored revocation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("running evaluator did not stop")
	}
	if _, err = os.Stat(filepath.Join(v.Project, "after-revocation")); !os.IsNotExist(err) {
		t.Fatal("revoked evaluator continued", err)
	}
	if _, _, err = m.runRepairProgram(ctx, v, "tests"); err == nil {
		t.Fatal("revoked owner still executed acceptance code")
	}
}
