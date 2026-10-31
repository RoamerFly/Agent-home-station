package pluginruntime

import (
	"bytes"
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

func TestExecutorAcceptsDeviceOnlyAsHostMediatedPermission(t *testing.T) {
	called := false
	e := Executor{Resolve: func(context.Context, pluginhost.Package, pluginhost.Grant) (LaunchSpec, error) {
		called = true
		return LaunchSpec{}, errors.New("synthetic resolver stop")
	}}
	p := pluginhost.Package{ID: "sample", Version: "1", Status: "ready", Platforms: []string{runtime.GOOS}, Runtime: pluginhost.Runtime{Transport: "mcp-stdio"}, Permissions: []string{"device"}}
	g := pluginhost.Grant{Account: "a", Plugin: p.ID, Version: p.Version, Permissions: p.Permissions}
	if _, err := e.Start(context.Background(), p, g); err == nil || !called {
		t.Fatal("device host-proxy permission was rejected before the host resolver")
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

func TestSameVerifiedPackageNormalizesOptionalCollections(t *testing.T) {
	dir := t.TempDir()
	want := readyFixture(t, dir)
	got, err := pluginhost.Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !sameVerifiedPackage(want, got) {
		t.Fatal("equivalent verified package rejected due to JSON collection representation")
	}
	got.Version = "changed"
	if sameVerifiedPackage(want, got) {
		t.Fatal("changed package identity accepted")
	}
}

func TestReadOnlyResourceRegistrationIsBoundAndImmutable(t *testing.T) {
	root := t.TempDir()
	code := filepath.Join(root, "code")
	data := filepath.Join(root, "data")
	resourceRoot := filepath.Join(root, "approved-model")
	for _, dir := range []string{code, data, resourceRoot} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := filepath.Join(resourceRoot, "model.manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"name":"synthetic-model","files":["model.bin"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resourceRoot, "model.bin"), []byte("synthetic model bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(`{"name":"synthetic-model","files":["model.bin"]}`))
	p := pluginhost.Package{ID: "media-understanding", Version: "0.1.1"}
	g := pluginhost.Grant{Account: "account-a", Plugin: p.ID, Version: p.Version}
	registered := ReadOnlyResource{
		ID:             "synthetic-model",
		Root:           resourceRoot,
		Account:        g.Account,
		Plugin:         p.ID,
		PackageVersion: p.Version,
		ManifestPath:   manifest,
		ManifestSHA256: hex.EncodeToString(sum[:]),
	}
	spec := LaunchSpec{PackageDir: code, DataDir: data, Resources: []ReadOnlyResource{registered}}
	if err := validateSpec(spec); err != nil {
		t.Fatal(err)
	}
	if err := validateResources(p, g, spec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"name":"synthetic-model","files":["replacement.bin"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if validateResources(p, g, spec) == nil {
		t.Fatal("changed resource manifest accepted")
	}
	if err := os.WriteFile(manifest, []byte(`{"name":"synthetic-model","files":["model.bin"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(resourceRoot, "oversized.manifest.json")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte{'x'}, int(maxResourceManifestBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readResourceManifest(oversized); err == nil {
		t.Fatal("oversized resource manifest accepted")
	}
	registrationDir := filepath.Join(root, "registrations")
	if err := os.Mkdir(registrationDir, 0700); err != nil {
		t.Fatal(err)
	}
	externalManifest := filepath.Join(registrationDir, "approved-model.json")
	if err := os.WriteFile(externalManifest, []byte(`{"name":"synthetic-model","files":["model.bin"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	registeredOutsideRoot := spec
	registeredOutsideRoot.Resources = append([]ReadOnlyResource(nil), spec.Resources...)
	registeredOutsideRoot.Resources[0].ManifestPath = externalManifest
	if err := validateResources(p, g, registeredOutsideRoot); err != nil {
		t.Fatal("host-only external registration manifest rejected", err)
	}

	cases := []struct {
		name   string
		mutate func(*LaunchSpec)
	}{
		{"foreign account", func(s *LaunchSpec) { s.Resources[0].Account = "account-b" }},
		{"foreign plugin", func(s *LaunchSpec) { s.Resources[0].Plugin = "music" }},
		{"foreign version", func(s *LaunchSpec) { s.Resources[0].PackageVersion = "0.1.2" }},
		{"changed manifest digest", func(s *LaunchSpec) { s.Resources[0].ManifestSHA256 = strings.Repeat("0", 64) }},
		{"manifest in package directory", func(s *LaunchSpec) { s.Resources[0].ManifestPath = filepath.Join(code, "main.py") }},
		{"private root", func(s *LaunchSpec) { s.Resources[0].Root = data }},
		{"duplicate resource identity", func(s *LaunchSpec) { s.Resources = append(s.Resources, s.Resources[0]) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := spec
			candidate.Resources = append([]ReadOnlyResource(nil), spec.Resources...)
			tc.mutate(&candidate)
			if validateResources(p, g, candidate) == nil {
				t.Fatal("invalid resource registration accepted")
			}
		})
	}

	linked := filepath.Join(root, "linked-resource")
	if err := os.Symlink(resourceRoot, linked); err == nil {
		candidate := spec
		candidate.Resources = append([]ReadOnlyResource(nil), spec.Resources...)
		candidate.Resources[0].Root = linked
		if validateResources(p, g, candidate) == nil {
			t.Fatal("symlinked resource root accepted")
		}
	}
}

func TestInstalledExecutorAcceptsResourcesOnlyFromHostResolver(t *testing.T) {
	store, installed := installedFixture(t)
	var err error
	installed, err = store.SetEnabled(context.Background(), installed.Package.ID, installed.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	grant := pluginhost.Grant{Account: store.Account(), Plugin: installed.Package.ID, Version: installed.Package.Version, Permissions: installed.Permissions}
	called := 0
	registered := ReadOnlyResource{ID: "model-a", Root: `C:\\synthetic-host-owned-resource`}
	executor := InstalledExecutor(store, Options{ResourceRoots: func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) ([]ReadOnlyResource, error) {
		called++
		if p.ID != installed.Package.ID || p.Version != installed.Package.Version || g.Account != grant.Account || g.Plugin != grant.Plugin || g.Version != grant.Version || len(g.Permissions) != len(grant.Permissions) {
			t.Fatal("resource resolver received unbound plugin scope")
		}
		return []ReadOnlyResource{registered}, nil
	}, HostMetadata: func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{"local_asr": json.RawMessage(`{"profile_id":"synthetic","resource_version":"v1"}`)}, nil
	}})
	spec, err := executor.Resolve(context.Background(), installed.Package, grant)
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || len(spec.Resources) != 1 || spec.Resources[0] != registered || string(spec.HostMetadata["local_asr"]) != `{"profile_id":"synthetic","resource_version":"v1"}` {
		t.Fatal("host resource registration was not carried into launch spec")
	}
	// A deployment that does not explicitly register a resource exposes none.
	withoutRegistration := InstalledExecutor(store, Options{})
	spec, err = withoutRegistration.Resolve(context.Background(), installed.Package, grant)
	if err != nil || len(spec.Resources) != 0 {
		t.Fatal("unregistered resource was exposed", err, spec.Resources)
	}
	denied := InstalledExecutor(store, Options{ResourceRoots: func(context.Context, pluginhost.Package, pluginhost.Grant) ([]ReadOnlyResource, error) {
		return nil, errors.New("host resource registry unavailable")
	}})
	if _, err := denied.Resolve(context.Background(), installed.Package, grant); err == nil {
		t.Fatal("resource registry failure did not fail closed")
	}
	metadataDenied := InstalledExecutor(store, Options{HostMetadata: func(context.Context, pluginhost.Package, pluginhost.Grant) (map[string]json.RawMessage, error) {
		return nil, errors.New("host metadata registry unavailable")
	}})
	if _, err := metadataDenied.Resolve(context.Background(), installed.Package, grant); err == nil {
		t.Fatal("metadata registry failure did not fail closed")
	}
}

const sandboxFixture = `import json, os, sys
config={}
host_metadata={}
for line in sys.stdin:
 q=json.loads(line)
 if 'id' not in q: continue
 r={}
 if q['method']=='initialize':
  config=q.get('params',{}).get('_meta',{}).get('wechat/configuration',{})
  host_metadata=q.get('params',{}).get('_meta',{}).get('wechat/host_metadata',{})
  r={'protocolVersion':'2025-06-18'}
 if q['method']=='tools/call':
  p=q['params']; a=p.get('arguments',{}); n=p['name']
  if n=='echo': r={'text':a['text'],'secret':os.environ.get('PRIVATE_PLUGIN_TEST_TOKEN')}
  elif n=='configuration': r=config

  elif n=='host_metadata': r=host_metadata
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
	resourceRoot := filepath.Join(root, "approved-model")
	sidecarRoot := filepath.Join(root, "sidecars")
	os.Mkdir(code, 0700)
	os.Mkdir(data, 0700)
	os.Mkdir(resourceRoot, 0700)
	os.Mkdir(sidecarRoot, 0700)
	os.WriteFile(outside, []byte("synthetic private boundary"), 0600)
	resourceManifest := filepath.Join(resourceRoot, "model.manifest.json")
	resourceFile := filepath.Join(resourceRoot, "model.bin")
	sidecarFile := filepath.Join(sidecarRoot, "vad.onnx")
	resourceManifestBytes := []byte(`{"name":"synthetic-model","files":["model.bin"]}`)
	if err := os.WriteFile(resourceManifest, resourceManifestBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resourceFile, []byte("synthetic model bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecarFile, []byte("synthetic vad bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	resourceHash := sha256.Sum256(resourceManifestBytes)
	p := readyFixture(t, code)
	g := pluginhost.Grant{Account: "a", Plugin: p.ID, Version: p.Version}
	spec := LaunchSpec{PackageDir: code, DataDir: data, Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), RuntimeRoots: []string{filepath.Dir(python)}, WindowsHelper: helper, Resources: []ReadOnlyResource{{ID: "synthetic-model", Root: resourceRoot, Account: g.Account, Plugin: p.ID, PackageVersion: p.Version, ManifestPath: resourceManifest, ManifestSHA256: hex.EncodeToString(resourceHash[:])}, {ID: "synthetic-vad", Root: sidecarFile, Kind: "file", Account: g.Account, Plugin: p.ID, PackageVersion: p.Version, ManifestPath: resourceManifest, ManifestSHA256: hex.EncodeToString(resourceHash[:])}}, HostMetadata: map[string]json.RawMessage{"local_asr": json.RawMessage(`{"profile_id":"synthetic","resource_version":"` + strings.Repeat("a", 64) + `"}`)}}
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
	if r := call("host_metadata", map[string]string{}); r["local_asr"].(map[string]any)["profile_id"] != "synthetic" {
		t.Fatal("trusted initializer metadata missing", r)
	}
	if r := call("read", map[string]string{"path": outside}); r["denied"] != true {
		t.Fatal("other account file was readable")
	}
	if r := call("read", map[string]string{"path": resourceFile}); r["denied"] != false || r["text"] != "synthetic model bytes" {
		t.Fatal("approved resource was unavailable", r)
	}
	if r := call("write", map[string]string{"path": resourceFile}); r["denied"] != true {
		t.Fatal("approved resource was writable")
	}
	if r := call("read", map[string]string{"path": sidecarFile}); r["denied"] != false || r["text"] != "synthetic vad bytes" {
		t.Fatal("approved sidecar file was unavailable", r)
	}
	if r := call("write", map[string]string{"path": sidecarFile}); r["denied"] != true {
		t.Fatal("approved sidecar file was writable")
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
