package devicebroker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/mcptools"
)

func TestRuntimeRegistrationRejectsRawOverridesAndChangedProof(t *testing.T) {
	o, paths := fixtureOptions(t)
	writeJSON(t, paths.config, validProfile(paths.adb))
	o.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	launch := o.Registration.launcher(o.Account, paths.storage, func() bool { return true })
	for _, c := range []mcptools.ServerConfig{{ID: "device", PluginID: "device-fixture", Command: "untrusted-command"}, {ID: "device", PluginID: "device-fixture", Args: []string{"untrusted"}}, {ID: "device", PluginID: "device-fixture", Env: map[string]string{"SECRET": "untrusted"}}, {ID: "device", PluginID: "other-package"}} {
		if _, err := launch(t.Context(), c); err == nil {
			t.Fatal("raw override reached runtime")
		}
	}
	o.Registration = registrationForAccount(t, o.Registration, "another-account")
	tracked := &countingADBServer{}
	o.ADBServer = tracked
	if _, err := Open(t.Context(), o); !errors.Is(err, ErrNeedsMigration) || tracked.calls != 0 {
		t.Fatal("foreign registration acquired ADB lease", err, tracked.calls)
	}
	o.Registration = registrationForAccount(t, o.Registration, o.Account)
	if err := os.WriteFile(paths.script, []byte("unapproved replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), o); err == nil || tracked.calls != 0 {
		t.Fatal("changed registered package acquired ADB lease", err, tracked.calls)
	}
}

const isolatedBridgeFixture = `import json,os,sys,time
for line in sys.stdin:
 q=json.loads(line)
 if 'id' not in q:continue
 method=q.get('method');r={}
 if method=='initialize':r={'protocolVersion':'2025-06-18'}
 elif method=='tools/list':r={'tools':[{'name':'observe','inputSchema':{'type':'object'}}]}
 elif method=='tools/call':
  a=q['params'].get('arguments',{})
  if a.get('block'):time.sleep(30)
  denied=False
  try:
   with open(a['outside'],'rb') as f:f.read()
  except (OSError,KeyError):denied=True
  text=json.dumps({'secret_present':'DEVICEBROKER_HOST_SECRET' in os.environ,'outside_denied':denied})
  r={'content':[{'type':'text','text':text}]}
 print(json.dumps({'jsonrpc':'2.0','id':q['id'],'result':r}),flush=True)
`

// This is a real mandatory-sandbox transport test, with an indexed synthetic
// entrypoint and fake ADB manager. It never imports the native bridge or ADB.
func TestLiveRegisteredDeviceBrokerUsesSandboxedStreamAndConfirmsCancellation(t *testing.T) {
	if os.Getenv("WECHAT_STREAM_SANDBOX_TEST") != "1" {
		t.Skip("opt-in project-local isolated synthetic bridge")
	}
	if runtime.GOOS != "windows" {
		t.Skip("project-local Windows LPAC runtime")
	}
	python, err := filepath.Abs("../../dist/windows-native/runtimes/windows-python-cli/python.exe")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	o, paths := fixtureOptions(t)
	if err = os.WriteFile(paths.script, []byte(isolatedBridgeFixture), 0600); err != nil {
		t.Fatal(err)
	}
	r := fixtureRegistration(t, paths.packageDir, python, o.Account)
	environment := RuntimeEnvironment{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}
	o.Registration, err = NewRuntimeRegistration(t.Context(), paths.packageDir, r.grant, environment)
	if err != nil {
		t.Fatal(err)
	}
	o.Python = python
	writeJSON(t, paths.config, validProfile(paths.adb))
	o.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	lease := &trackedADBLease{}
	o.ADBServer = trackedADBServer{lease: lease}
	outside := filepath.Join(t.TempDir(), "not-mounted.txt")
	if err = os.WriteFile(outside, []byte("synthetic private outside data"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVICEBROKER_HOST_SECRET", "must-not-reach-child")
	b, err := Open(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	args, _ := json.Marshal(map[string]any{"device_id": "allowed", "outside": outside})
	result, err := b.Call(ctx, "observe", args)
	if err != nil || !strings.Contains(result, `"secret_present": false`) || !strings.Contains(result, `"outside_denied": true`) {
		t.Fatal("isolated bridge boundary failed", result, err)
	}
	blocked, cancelBlocked := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancelBlocked()
	args, _ = json.Marshal(map[string]any{"device_id": "allowed", "block": true})
	started := time.Now()
	_, err = b.Call(blocked, "observe", args)
	if err == nil || time.Since(started) > 8*time.Second {
		t.Fatal("cancel did not confirm sandbox tree close", err, time.Since(started))
	}
	if err = b.Close(); err != nil || lease.closes != 1 {
		t.Fatal("broker close/ADB lease cleanup not confirmed", err, lease.closes)
	}
}
