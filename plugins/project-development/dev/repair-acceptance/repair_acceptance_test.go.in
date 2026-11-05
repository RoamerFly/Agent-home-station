package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepairAcceptanceRequiresAdminAndExistingOwnerAuthority(t *testing.T) {
	f, _ := trustedAgent(t)
	if code, _, _ := f.request("POST", "/api/agent/repair-tests", map[string]string{"recipient": "wxid_owner"}, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/agent/repair-tests", map[string]string{"recipient": "wxid_owner"}, false, false); code != 403 {
		t.Fatal("acceptance mutation did not require CSRF", code)
	}
	for _, recipient := range []string{"wxid_member", "wxid_owner"} {
		code, _, _ := f.request("POST", "/api/agent/repair-tests", map[string]string{"recipient": recipient}, true, true)
		if code != 400 {
			t.Fatal("test elevated absent execution permission", recipient, code)
		}
	}
	if _, e := f.manager.loadRepairRun("../agent-config"); e == nil {
		t.Fatal("path traversal admitted")
	}
}
func TestRepairAcceptanceFixturesFollowupAndIndependentGrader(t *testing.T) {
	requireRepairSandbox(t)
	if _, e := exec.LookPath(repairPython()); e != nil {
		t.Skip("Python required for acceptance fixture")
	}
	f, _ := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	c := f.manager.agent.config()
	c.Host.Mode = "full"
	if e := f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	v, e := f.manager.startRepairRun(ctx, "wxid_owner")
	if e != nil || !v.BaselineFailed {
		t.Fatal(v, e)
	}
	task := f.manager.agent.list("test", "wxid_owner")[0]
	if !task.OwnerRequested || !strings.Contains(task.Checkpoint, "不是真实微信入站") {
		t.Fatal("test masquerades as a WeChat packet", task)
	}
	f.login()
	code, _, _ := f.request("POST", "/api/agent/repair-tests/"+v.ID+"/followup", map[string]string{}, true, true)
	if code != 409 {
		t.Fatal("followup preceded first repair", code)
	}
	source := filepath.Join(v.Project, "shipping.py")
	b, _ := os.ReadFile(source)
	os.WriteFile(source, []byte(strings.Replace(string(b), "net > Decimal", "net >= Decimal", 1)), 0600)
	code, body, _ := f.request("POST", "/api/agent/repair-tests/"+v.ID+"/followup", map[string]string{}, true, true)
	if code != 202 {
		t.Fatal(code, string(body))
	}
	var follow repairTestRun
	json.Unmarshal(body, &follow)
	if !follow.FollowupFailed {
		t.Fatal("followup error not reproduced")
	}
	code, body, _ = f.request("GET", "/api/agent/repair-tests/"+v.ID, nil, false, false)
	var status map[string]any
	json.Unmarshal(body, &status)
	if code != 200 || status["independent_grader_passed"] != false {
		t.Fatal("grader accepted broken receipt", code, status)
	}
	os.WriteFile(filepath.Join(v.Project, "receipt.py"), []byte("def describe(quote):\n    return '实付' + quote['total'] + '元'\n"), 0600)
	_, body, _ = f.request("GET", "/api/agent/repair-tests/"+v.ID, nil, false, false)
	json.Unmarshal(body, &status)
	if status["independent_grader_passed"] != true || status["tests_unchanged"] != true {
		t.Fatal(status)
	}
	follow.Stage = 3
	if e := f.manager.saveRepairRun(follow); e != nil {
		t.Fatal(e)
	}
	_, body, _ = f.request("GET", "/api/agent/repair-tests/"+v.ID, nil, false, false)
	json.Unmarshal(body, &status)
	if !strings.Contains(status["grader_output"].(string), `"passed": 16`) {
		t.Fatal("lifecycle stage dropped receipt validation", status)
	}
	os.WriteFile(filepath.Join(v.Project, "tests/test_quote.py"), []byte("# tampered\n"), 0600)
	_, body, _ = f.request("GET", "/api/agent/repair-tests/"+v.ID, nil, false, false)
	json.Unmarshal(body, &status)
	if status["tests_unchanged"] != false {
		t.Fatal("test modification not detected")
	}
}
