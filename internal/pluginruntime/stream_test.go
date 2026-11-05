package pluginruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
)

func TestStreamUsesSameVerifiedPackageAndSandboxAdmissionAsExecutor(t *testing.T) {
	code, data := t.TempDir(), t.TempDir()
	p := readyFixture(t, code)
	g := pluginhost.Grant{Account: "synthetic", Plugin: p.ID, Version: p.Version}
	resolved := 0
	e := &Executor{Resolve: func(context.Context, pluginhost.Package, pluginhost.Grant) (LaunchSpec, error) {
		resolved++
		return LaunchSpec{PackageDir: code, DataDir: data}, nil
	}}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.StartStream(cancelled, p, g); !errors.Is(err, context.Canceled) || resolved != 0 {
		t.Fatal("cancelled stream reached resolver", err, resolved)
	}
	wrong := g
	wrong.Plugin = "different"
	if _, err := e.StartStream(t.Context(), p, wrong); err == nil || resolved != 0 {
		t.Fatal("unbound grant admitted", err, resolved)
	}
	if _, err := e.StartStream(t.Context(), p, g); err == nil || !strings.Contains(err.Error(), "Python runtime") {
		t.Fatal("unregistered runtime admitted", err)
	}
	if err := os.WriteFile(filepath.Join(code, "main.py"), []byte("# unapproved replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.StartStream(t.Context(), p, g); err == nil {
		t.Fatal("changed package admitted")
	}
}

const streamMCPFixture = `import json,os,sys
for line in sys.stdin:
 q=json.loads(line)
 if 'id' not in q:continue
 method=q['method'];r={}
 if method=='initialize':r={'protocolVersion':'2025-06-18'}
 elif method=='tools/list':r={'tools':[{'name':'echo','inputSchema':{'type':'object'}}]}
 elif method=='tools/call':
  args=q['params'].get('arguments',{})
  denied=False
  try:
   with open(args['outside'],'rb') as file:file.read()
  except (OSError,KeyError):denied=True
  text=json.dumps({'echo':args.get('text'),'ambient_secret':os.environ.get('WECHAT_STREAM_TEST_SECRET',''),'outside_denied':denied})
  r={'content':[{'type':'text','text':text}]}
 print(json.dumps({'jsonrpc':'2.0','id':q['id'],'result':r}),flush=True)
`

// This opt-in integration uses the project-local Python distribution only.
// The fixture contains no providers, accounts, models, device or WeChat access.
func TestLiveSandboxedMCPStreamRoundTrip(t *testing.T) {
	if os.Getenv("WECHAT_STREAM_SANDBOX_TEST") != "1" {
		t.Skip("opt-in synthetic mandatory-sandbox stream test")
	}
	if runtime.GOOS != "windows" {
		t.Skip("project-local Windows LPAC runtime test")
	}
	python, err := filepath.Abs("../../dist/windows-native/runtimes/windows-python-cli/python.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(python); err != nil {
		t.Fatal("project-local runtime missing", err)
	}
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	code, data, outside := t.TempDir(), t.TempDir(), t.TempDir()
	p := readyFixture(t, code)
	if err = os.WriteFile(filepath.Join(code, "main.py"), []byte(streamMCPFixture), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(streamMCPFixture))
	p.Files["main.py"] = hex.EncodeToString(sum[:])
	manifest, _ := json.Marshal(p)
	if err = os.WriteFile(filepath.Join(code, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "unmounted-private.txt")
	if err = os.WriteFile(outsideFile, []byte("synthetic unmounted private data"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_STREAM_TEST_SECRET", "synthetic ambient secret must not be inherited")
	e := &Executor{Resolve: func(context.Context, pluginhost.Package, pluginhost.Grant) (LaunchSpec, error) {
		return LaunchSpec{PackageDir: code, DataDir: data, Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}, nil
	}}
	g := pluginhost.Grant{Account: "synthetic", Plugin: p.ID, Version: p.Version}
	launcher := mcptools.WithStdioLauncher(func(ctx context.Context, c mcptools.ServerConfig) (mcptools.StdioProcess, error) {
		if c.ID != "registered" {
			return nil, errors.New("runtime registration missing")
		}
		return e.StartStream(ctx, p, g)
	})
	hub, err := mcptools.New([]mcptools.ServerConfig{{ID: "registered", Enabled: true, Transport: "stdio", Command: "must-not-run-raw", Args: []string{"must-not-forward"}, Env: map[string]string{"WECHAT_STREAM_TEST_SECRET": "configured env must not override runtime"}}}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	for _, callCtx := range []context.Context{ctx, mcptools.WithSessionScope(ctx, "synthetic-account-thread")} {
		tools, err := hub.List(callCtx)
		if err != nil || len(tools) != 1 || tools[0].Name != "registered__echo" {
			t.Fatal("sandboxed MCP discovery failed", tools, err)
		}
		args, _ := json.Marshal(map[string]string{"text": "synthetic echo", "outside": outsideFile})
		raw, err := hub.Call(callCtx, "registered__echo", args)
		if err != nil {
			t.Fatal("sandboxed MCP call failed", err)
		}
		var observed struct {
			Echo    string `json:"echo"`
			Ambient string `json:"ambient_secret"`
			Denied  bool   `json:"outside_denied"`
		}
		if json.Unmarshal([]byte(raw), &observed) != nil || observed.Echo != "synthetic echo" || observed.Ambient != "" || !observed.Denied {
			t.Fatal("sandbox boundary failed", raw)
		}
	}
	if err = hub.Close(); err != nil {
		t.Fatal("sandbox tree close unconfirmed", err)
	}
}
