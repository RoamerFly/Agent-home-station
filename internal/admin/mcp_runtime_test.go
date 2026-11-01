package admin

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

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
)

const registeredMCPFixture = `import json,os,sys
from pathlib import Path
count=0
for line in sys.stdin:
 q=json.loads(line)
 if 'id' not in q:continue
 method=q['method'];r={}
 if method=='initialize':r={'protocolVersion':'2025-06-18'}
 elif method=='tools/list':r={'tools':[{'name':'echo','inputSchema':{'type':'object'}}]}
 elif method=='tools/call':
  count+=1
  Path('invocations.txt').write_text(str(count))
  text=json.dumps({'cwd':os.getcwd(),'calls':count,'echo':q['params'].get('arguments',{}).get('text'),'ambient':os.environ.get('WECHAT_MCP_ADMIN_SECRET','')})
  r={'content':[{'type':'text','text':text}]}
 print(json.dumps({'jsonrpc':'2.0','id':q['id'],'result':r}),flush=True)
`

func installedMCPRuntimeFixture(t *testing.T, m *Manager) (*pluginhost.Store, pluginhost.InstalledPackage, mcptools.ServerConfig) {
	t.Helper()
	source := t.TempDir()
	p := pluginhost.Package{SchemaVersion: 1, ID: "mcp-echo", Name: "Synthetic MCP runtime", Version: "1.0.0", HostProtocol: 1, Status: "ready", Platforms: []string{runtime.GOOS + "-" + runtime.GOARCH}, Runtime: pluginhost.Runtime{Transport: "mcp-stdio", Entrypoint: "main.py"}, Files: map[string]string{}}
	files := map[string]string{"main.py": registeredMCPFixture, "config.schema.json": `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}`, "ui.schema.json": "{}", "capabilities.json": `{"version":1,"capabilities":[{"id":"echo","implementation":"ready","description":"Synthetic echo","permissions":[],"input_schema":{"type":"object"},"output_schema":{"type":"object"},"discoverable":true}]}`}
	for _, name := range []string{"README.md", "PERMISSIONS.md", "MIGRATION.md", "CHANGELOG.md"} {
		files[name] = "# Synthetic test only\n"
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		p.Files[name] = hex.EncodeToString(sum[:])
	}
	raw, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := m.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	i, err := store.Install(t.Context(), prepared.Token, prepared.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	i, err = store.SetEnabled(t.Context(), p.ID, i.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	server := mcptools.ServerConfig{ID: "registered", Transport: "stdio", PluginID: p.ID, Enabled: true, Tools: []string{"echo"}}
	m.mu.Lock()
	m.mcp = mcpDefaults()
	m.mcp.Servers = []mcptools.ServerConfig{server}
	m.mu.Unlock()
	return store, i, server
}

func TestMCPRuntimeRequiresCurrentInstalledRegistrationAndJSONAuthority(t *testing.T) {
	f := setup(t)
	store, installed, server := installedMCPRuntimeFixture(t, f.manager)
	broker := &mcpRuntimeBroker{manager: f.manager, account: f.manager.native.Account(), bindings: map[string]mcpRuntimeBinding{}}
	if err := broker.guard(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	implicit := server
	implicit.Transport = ""
	f.manager.mu.Lock()
	f.manager.mcp.Servers = []mcptools.ServerConfig{implicit}
	f.manager.mu.Unlock()
	if err := broker.guard(t.Context(), implicit.Normalized()); err != nil {
		t.Fatal("omitted transport/timeout changed registration authority", err)
	}
	f.manager.mu.Lock()
	f.manager.mcp.Servers = []mcptools.ServerConfig{server}
	f.manager.mu.Unlock()
	legacy := server
	legacy.PluginID = ""
	legacy.Command = "must-not-execute"
	if _, err := broker.launch(t.Context(), legacy); err == nil {
		t.Fatal("legacy command fell back to host execution")
	}
	missing := server
	missing.PluginID = "missing-runtime"
	f.manager.mu.Lock()
	f.manager.mcp.Servers = []mcptools.ServerConfig{missing}
	f.manager.mu.Unlock()
	if err := broker.guard(t.Context(), missing); err == nil || !strings.Contains(err.Error(), "package_unavailable") {
		t.Fatal("unregistered package admitted")
	}
	f.manager.mu.Lock()
	f.manager.mcp.Servers = []mcptools.ServerConfig{server}
	f.manager.mu.Unlock()
	if _, err := store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	if err := broker.guard(t.Context(), server); err == nil {
		t.Fatal("disabled backend retained authority")
	}
	f.manager.mu.Lock()
	f.manager.mcp.Servers = nil
	f.manager.mu.Unlock()
	if err := broker.guard(t.Context(), server); err == nil {
		t.Fatal("removed JSON connection retained authority")
	}
}

func TestMCPRuntimeLedgerRegistrationAndReplacementAuthority(t *testing.T) {
	for _, change := range []string{"version", "runtime", "disabled", "conflict"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			_, _, server := installedMCPRuntimeFixture(t, f.manager)
			// The declaration comes only from the live legacy ledger, with
			// transport/timeout omitted just as in an administrator descriptor.
			server.Transport = ""
			declaration := server
			declaration.ID = ""
			declaration.Enabled = false
			entry := pluginEntry{Installed: plugins.Installed{Enabled: true, Manifest: plugins.Manifest{ID: server.ID, Name: "Synthetic registered MCP", Version: "1", MCP: &declaration}}}
			if err := f.manager.plugins.save(entry); err != nil {
				t.Fatal(err)
			}
			f.manager.mu.Lock()
			f.manager.mcp.Servers = nil
			f.manager.mu.Unlock()
			access, _, err := f.manager.toolAccess()
			if err != nil {
				t.Fatal("toolAccess rejected registered ledger source", err)
			}
			defer access.hub.Close()
			broker := &mcpRuntimeBroker{manager: f.manager, account: f.manager.native.Account(), bindings: map[string]mcpRuntimeBinding{}}
			if err = broker.guard(t.Context(), server.Normalized()); err != nil {
				t.Fatal("live ledger runtime not authorized", err)
			}
			switch change {
			case "version":
				entry.Manifest.Version = "2"
			case "runtime":
				changed := *entry.Manifest.MCP
				changed.PluginID = "another-package"
				entry.Manifest.MCP = &changed
			case "disabled":
				entry.Enabled = false
			case "conflict":
				f.manager.mu.Lock()
				f.manager.mcp.Servers = []mcptools.ServerConfig{server}
				f.manager.mu.Unlock()
			}
			if change != "conflict" {
				if err = f.manager.plugins.save(entry); err != nil {
					t.Fatal(err)
				}
			}
			if err = broker.guard(t.Context(), server.Normalized()); err == nil {
				t.Fatal("changed ledger declaration retained old runtime authority", change)
			}
		})
	}
}

func TestLiveAdminRegisteredMCPRuntimeIsolationAndRevocation(t *testing.T) {
	if os.Getenv("WECHAT_STREAM_SANDBOX_TEST") != "1" {
		t.Skip("opt-in synthetic registered LPAC MCP test")
	}
	if runtime.GOOS != "windows" {
		t.Skip("project-local Windows LPAC runtime test")
	}
	python, err := filepath.Abs("../../dist/windows-native/runtimes/windows-python-cli/python.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(python); err != nil {
		t.Fatal("project-local Python required", err)
	}
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	options, _ := json.Marshal(pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}})
	t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", string(options))
	t.Setenv("WECHAT_MCP_ADMIN_SECRET", "synthetic ambient secret")
	for _, change := range []string{"disabled", "configuration", "json", "runtime-closed", "manager-closed"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			store, installed, server := installedMCPRuntimeFixture(t, f.manager)
			// Both transport and timeout are omitted in this valid JSON
			// registration; the Hub and authority guard must normalize alike.
			server.Transport = ""
			f.manager.mu.Lock()
			f.manager.mcp.Servers = []mcptools.ServerConfig{server}
			f.manager.mu.Unlock()
			hub, err := f.manager.newMCPHub([]mcptools.ServerConfig{server})
			if err != nil {
				t.Fatal(err)
			}
			defer hub.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			var directories []string
			for _, scope := range []string{"account-a/requester-a/thread-a", "account-a/requester-b/thread-a"} {
				callCtx := mcptools.WithSessionScope(ctx, scope)
				if _, err = hub.List(callCtx); err != nil {
					t.Fatal(err)
				}
				out, err := hub.Call(callCtx, "registered__echo", json.RawMessage(`{"text":"registered echo","path":"../../must-not-control-cwd"}`))
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					CWD     string `json:"cwd"`
					Calls   int    `json:"calls"`
					Echo    string `json:"echo"`
					Ambient string `json:"ambient"`
				}
				if json.Unmarshal([]byte(out), &result) != nil || result.Calls != 1 || result.Echo != "registered echo" || result.Ambient != "" {
					t.Fatal("registered MCP result invalid", out)
				}
				directories = append(directories, result.CWD)
			}
			base, err := store.PrivateDirectory(ctx, installed.Package.ID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.EqualFold(directories[0], directories[1]) || !pathWithin(base, directories[0]) || !pathWithin(base, directories[1]) || directories[0] == base {
				t.Fatal("scope directory not isolated", directories)
			}
			var idle mcptools.StdioProcess
			var idleDone chan error
			if change == "runtime-closed" || change == "manager-closed" {
				broker := &mcpRuntimeBroker{manager: f.manager, account: f.manager.native.Account(), bindings: map[string]mcpRuntimeBinding{}}
				idle, err = broker.launch(mcptools.WithSessionScope(ctx, "idle-watch"), server.Normalized())
				if err != nil {
					t.Fatal(err)
				}
				defer idle.Close()
				idleDone = make(chan error, 1)
				go func() { idleDone <- idle.Wait() }()
			}
			switch change {
			case "disabled", "configuration":
				installed, err = store.SetEnabled(ctx, installed.Package.ID, installed.Revision, false)
				if err != nil {
					t.Fatal(err)
				}
				if change == "configuration" {
					if _, err = store.SaveConfiguration(ctx, installed.Package.ID, pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"label": json.RawMessage(`"changed"`)}}); err != nil {
						t.Fatal(err)
					}
					installed, err = store.Installed(ctx, installed.Package.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = store.SetEnabled(ctx, installed.Package.ID, installed.Revision, true); err != nil {
						t.Fatal(err)
					}
				}
			case "json":
				f.manager.mu.Lock()
				f.manager.mcp.Servers = nil
				f.manager.mu.Unlock()
			case "runtime-closed":
				controller, _, err := f.manager.installedPluginRuntime()
				if err != nil {
					t.Fatal(err)
				}
				if err = controller.Close(); err != nil {
					t.Fatal(err)
				}
			case "manager-closed":
				if err = f.manager.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if idleDone != nil {
				select {
				case <-idleDone:
				case <-time.After(4 * time.Second):
					t.Fatal("idle registered runtime survived host/controller close")
				}
			}
			if _, err = hub.Call(mcptools.WithSessionScope(ctx, "account-a/requester-a/thread-a"), "registered__echo", json.RawMessage(`{"text":"must not execute"}`)); err == nil {
				t.Fatal("revoked runtime called")
			}
			for _, dir := range directories {
				raw, err := os.ReadFile(filepath.Join(dir, "invocations.txt"))
				if err != nil || string(raw) != "1" {
					t.Fatal("revoked callback executed or rewrote another scope", string(raw), err)
				}
			}
			if err = hub.Close(); err != nil {
				t.Fatal("revoked runtime close unconfirmed", err)
			}
		})
	}
}
