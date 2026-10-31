package pluginruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

func TestExecutorRejectsUnimplementedGrants(t *testing.T) {
	called := false
	e := Executor{Resolve: func(context.Context, pluginhost.Package, pluginhost.Grant) (LaunchSpec, error) {
		called = true
		return LaunchSpec{}, nil
	}}
	p := pluginhost.Package{ID: "sample", Version: "1", Status: "ready", Platforms: []string{runtime.GOOS}, Runtime: pluginhost.Runtime{Transport: "mcp-stdio"}, Permissions: []string{"gpu"}}
	g := pluginhost.Grant{Account: "a", Plugin: p.ID, Version: p.Version, Permissions: p.Permissions}
	if _, err := e.Start(context.Background(), p, g); err == nil || called {
		t.Fatal("unsupported capability reached launch")
	}
}
func TestLaunchMountsCannotWriteCode(t *testing.T) {
	root := t.TempDir()
	code := filepath.Join(root, "code")
	data := filepath.Join(root, "data")
	os.Mkdir(code, 0700)
	os.Mkdir(data, 0700)
	if err := validateSpec(LaunchSpec{PackageDir: code, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []LaunchSpec{{PackageDir: code, DataDir: root}, {PackageDir: root, DataDir: data}, {PackageDir: code, DataDir: data, RuntimeRoots: []string{root}}} {
		if validateSpec(s) == nil {
			t.Fatal("writable code overlap accepted")
		}
	}
}

const sandboxFixture = `import json, os, sys
config={}
for line in sys.stdin:
 q=json.loads(line)
 if 'id' not in q: continue
 r={}
 if q['method']=='initialize':
  config=q.get('params',{}).get('_meta',{}).get('wechat/configuration',{})
  r={'protocolVersion':'2025-06-18'}
 if q['method']=='tools/call':
  p=q['params']; a=p.get('arguments',{}); n=p['name']
  if n=='echo': r={'text':a['text'],'secret':os.environ.get('PRIVATE_PLUGIN_TEST_TOKEN')}
  elif n=='configuration': r=config
  elif n=='pid': r={'pid':os.getpid()}
  elif n=='read':
   try: r={'text':open(a['path']).read(),'denied':False}
   except OSError: r={'denied':True}
  elif n=='write':
   try:
    with open(a['path'],'w') as f: f.write('synthetic')
    r={'denied':False}
   except OSError: r={'denied':True}
  elif n=='block':
   import time; time.sleep(60)
 print(json.dumps({'jsonrpc':'2.0','id':q['id'],'result':r}),flush=True)
`

func readyFixture(t *testing.T, dir string) pluginhost.Package {
	t.Helper()
	p := pluginhost.Package{SchemaVersion: 1, ID: "sample", Name: "Synthetic isolated process", Version: "1", HostProtocol: 1, Status: "ready", Platforms: []string{runtime.GOOS}, Runtime: pluginhost.Runtime{Transport: "mcp-stdio", Entrypoint: "main.py"}, Files: map[string]string{}}
	files := map[string]string{"main.py": sandboxFixture}
	for _, name := range []string{"config.schema.json", "ui.schema.json", "capabilities.json", "README.md", "PERMISSIONS.md", "MIGRATION.md", "CHANGELOG.md"} {
		files[name] = "{}"
	}
	files["config.schema.json"] = `{"type":"object","properties":{"label":{"type":"string","default":"synthetic"},"api_key":{"type":"string","x-secret":true}},"additionalProperties":false}`
	files["capabilities.json"] = `{"version":1,"capabilities":[{"id":"echo","implementation":"ready","description":"Synthetic scoped text","permissions":[],"input_schema":{"type":"object"},"output_schema":{"type":"object"},"discoverable":true},{"id":"async","implementation":"ready","description":"Synthetic asynchronous task","permissions":[],"input_schema":{"type":"object"},"output_schema":{"type":"object"},"discoverable":true,"async":true}]}`
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		p.Files[name] = hex.EncodeToString(sum[:])
	}
	b, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestLiveIsolatedStdio(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in synthetic platform isolation test")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	root := t.TempDir()
	code := filepath.Join(root, "code")
	data := filepath.Join(root, "data")
	outside := filepath.Join(root, "other-account.txt")
	os.Mkdir(code, 0700)
	os.Mkdir(data, 0700)
	os.WriteFile(outside, []byte("synthetic private boundary"), 0600)
	p := readyFixture(t, code)
	g := pluginhost.Grant{Account: "a", Plugin: p.ID, Version: p.Version}
	spec := LaunchSpec{PackageDir: code, DataDir: data, Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}
	e := Executor{Resolve: func(context.Context, pluginhost.Package, pluginhost.Grant) (LaunchSpec, error) { return spec, nil }}
	t.Setenv("PRIVATE_PLUGIN_TEST_TOKEN", "synthetic-not-for-child")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := e.Start(ctx, p, g)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	call := func(name string, args map[string]string) map[string]any {
		t.Helper()
		b, _ := json.Marshal(args)
		raw, err := s.Call(ctx, invocation(), name, b)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if json.Unmarshal(raw, &out) != nil {
			t.Fatal("invalid synthetic result")
		}
		return out
	}
	if err = s.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if r := call("echo", map[string]string{"text": "合成 MCP 连通"}); r["text"] != "合成 MCP 连通" || r["secret"] != nil {
		t.Fatal("echo or environment isolation failed")
	}
	if r := call("read", map[string]string{"path": outside}); r["denied"] != true {
		t.Fatal("other account file was readable")
	}
	if r := call("write", map[string]string{"path": filepath.Join(code, "main.py")}); r["denied"] != true {
		t.Fatal("package was writable")
	}
	if r := call("write", map[string]string{"path": filepath.Join(data, "output.txt")}); r["denied"] != false {
		t.Fatal("own data was not writable")
	}
	timeout, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if _, err = s.Call(timeout, invocation(), "block", json.RawMessage(`{}`)); err == nil {
		t.Fatal("cancellation ignored")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Call(ctx, invocation(), "echo", json.RawMessage(`{}`)); err == nil || strings.Contains(err.Error(), "synthetic-not-for-child") {
		t.Fatal("closed session reused or secret leaked")
	}
}
