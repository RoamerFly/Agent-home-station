package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
)

func (m *Manager) repairExecution(v repairTestRun) (agenthost.Config, error) {
	p := &permittedTools{manager: m, trace: companion.Input{Channel: "wechat", Account: m.native.Account(), Thread: v.Recipient, Sender: v.Recipient, Mode: "direct"}}
	if !validRepairRun(v.ID) || !p.privileged() || !m.agent.config().TasksEnabled || m.agent.config().Host.Mode != "full" {
		return agenthost.Config{}, errors.New("repair_owner_execution_not_permitted")
	}
	expected := filepath.Join(p.executionContext().Conversation, "scratch", "repair-tests", v.ID)
	if v.Project != expected {
		return agenthost.Config{}, errors.New("repair_legacy_workspace_not_executed: original record retained; create a managed test")
	}
	return p.scopedHostConfig(json.RawMessage(`{}`))
}

func (m *Manager) openRepairProject(v repairTestRun) (*os.Root, error) {
	cfg, err := m.repairExecution(v)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cfg.Roots[0])
	if err != nil {
		return nil, err
	}
	defer root.Close()
	relative, err := filepath.Rel(cfg.Roots[0], v.Project)
	if err != nil {
		return nil, err
	}
	return root.OpenRoot(relative)
}

func repairFileHash(root *os.Root, name string) string {
	name = filepath.FromSlash(name)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	file, err := root.OpenFile(name, os.O_RDONLY|repairNonblockingFlag, 0)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, 65537))
	if err != nil || n > 65536 {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func createRepairFixture(root *os.Root, name, body string) error {
	name = filepath.FromSlash(name)
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		hash := sha256.Sum256([]byte(body))
		if repairFileHash(root, name) == hex.EncodeToString(hash[:]) {
			return nil
		}
		return errors.New("repair_fixture_conflict_existing_file_retained")
	}
	if err != nil {
		return err
	}
	_, err = io.WriteString(file, body)
	if err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	return closed
}

// The fixed runner, fixtures, and imported Agent-edited modules all execute in
// the same mandatory namespace. A failed executor is not a reproduced bug.
const repairRunner = `import contextlib,io,json,sys,traceback,unittest
sys.path.insert(0,sys.argv[1])
output=io.StringIO()
code=0
with contextlib.redirect_stdout(output),contextlib.redirect_stderr(output):
 try:
  if sys.argv[2]=='tests':
   result=unittest.TextTestRunner(stream=output,verbosity=2).run(unittest.defaultTestLoader.discover('tests'))
   code=0 if result.wasSuccessful() else 1
  else:
   source=sys.argv[3]
   sys.argv=[sys.argv[0],sys.argv[1],sys.argv[2]]
   exec(compile(source,'<independent-repair-grader>','exec'),{})
 except BaseException:
  code=1
  traceback.print_exc(file=output)
print(json.dumps({'exit_code':code,'output':output.getvalue()[-4000:]}))
`

func (m *Manager) runRepairProgram(ctx context.Context, v repairTestRun, mode string) (int, string, error) {
	cfg, err := m.repairExecution(v)
	if err != nil {
		return 0, "", err
	}
	root, err := m.openRepairProject(v)
	if err != nil {
		return 0, "", err
	}
	root.Close()
	args := []string{repairPython(), "-I", "-c", repairRunner, v.Project, mode}
	if mode != "tests" {
		args = append(args, repairGrader)
	}
	bounded, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-bounded.Done():
				return
			case <-ticker.C:
				access := &permittedTools{manager: m, trace: companion.Input{Account: m.native.Account(), Thread: v.Recipient, Sender: v.Recipient}}
				policy := m.agent.config()
				if !access.privileged() || !policy.TasksEnabled || policy.Host.Mode != "full" {
					cancel()
					return
				}
			}
		}
	}()
	raw, err := agenthost.RunIsolated(bounded, cfg, v.Project, args)
	if err != nil {
		return 0, "", err
	}
	var result struct {
		Exit   *int   `json:"exit_code"`
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Exit == nil || (*result.Exit != 0 && *result.Exit != 1) {
		return 0, "", errors.New("repair_executor_result_invalid")
	}
	return *result.Exit, result.Output, nil
}
