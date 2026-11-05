package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
)

type mcpRuntimeBinding struct {
	Account               string
	Installed             pluginhost.InstalledPackage
	ConfigurationRevision int
	Deployment            string
	Authority             string
}

type mcpRuntimeBroker struct {
	manager  *Manager
	account  string
	mu       sync.Mutex
	bindings map[string]mcpRuntimeBinding
}

// Only this host constructor enables local MCP launch. JSON plugin_id selects
// an already installed and enabled package; raw command/args/env never launch.
func (m *Manager) newMCPHub(servers []mcptools.ServerConfig) (*mcptools.Hub, error) {
	broker := &mcpRuntimeBroker{manager: m, account: m.native.Account(), bindings: map[string]mcpRuntimeBinding{}}
	return mcptools.New(servers, mcptools.WithStdioLauncher(broker.launch), mcptools.WithServerGuard(broker.guard))
}

func (b *mcpRuntimeBroker) read(ctx context.Context, c mcptools.ServerConfig, verify bool) (mcpRuntimeBinding, *pluginhost.Store, error) {
	var empty mcpRuntimeBinding
	if err := ctx.Err(); err != nil {
		return empty, nil, err
	}
	if c.Transport != "stdio" || c.PluginID == "" || c.Command != "" || len(c.Args) > 0 || len(c.Env) > 0 {
		return empty, nil, errors.New("mcp_stdio_installed_runtime_required")
	}
	m := b.manager
	if b.account == "" || m.native.Account() != b.account {
		return empty, nil, errors.New("mcp_runtime_account_changed")
	}
	m.packageMu.Lock()
	closed, runtime := m.packagesClosed, m.packageRuntime
	m.packageMu.Unlock()
	if closed || runtime != nil && runtime.Closed() {
		return empty, nil, errors.New("mcp_runtime_closed")
	}
	// Resolve both live sources exactly as toolAccess does. A Hub's original
	// merged list is not authority after JSON settings or the ledger changes.
	m.mu.Lock()
	settings := cloneMCP(m.mcp)
	m.mu.Unlock()
	legacyItems := []plugins.Installed{}
	if m.plugins != nil {
		legacyItems = m.plugins.installed()
	}
	legacy, err := plugins.New(legacyItems)
	if err != nil {
		return empty, nil, errors.New("mcp_runtime_declarations_invalid")
	}
	declared, err := legacy.MCPServers()
	if err != nil {
		return empty, nil, errors.New("mcp_runtime_declarations_invalid")
	}
	all := append(settings.Servers, declared...)
	seen := map[string]bool{}
	for index := range all {
		all[index] = all[index].Normalized()
		if seen[all[index].ID] {
			return empty, nil, errors.New("mcp_runtime_server_conflict")
		}
		seen[all[index].ID] = true
	}
	if _, err := mcptools.New(all); err != nil {
		return empty, nil, errors.New("mcp_runtime_declarations_invalid")
	}
	configured := false
	authority := ""
	for index, current := range all {
		if current.ID == c.ID && current.Enabled && reflect.DeepEqual(current, c) {
			configured = true
			var raw []byte
			if index < len(settings.Servers) {
				raw, _ = json.Marshal([]any{"json", current})
			} else {
				for _, item := range legacyItems {
					if item.Manifest.ID == current.ID {
						raw, _ = json.Marshal([]any{"ledger", item})
						break
					}
				}
			}
			authority = namedSpeechSHA(raw)
			break
		}
	}
	if !settings.Enabled || !configured {
		return empty, nil, errors.New("mcp_runtime_configuration_changed")
	}
	store, err := m.packageStore()
	if err != nil {
		return empty, nil, err
	}
	i, err := store.Installed(ctx, c.PluginID)
	if err != nil || !i.Enabled || i.Removed || i.Package.Runtime.Transport != "mcp-stdio" || store.Account() != b.account {
		return empty, nil, errors.New("mcp_runtime_package_unavailable")
	}
	config, err := store.Configuration(ctx, c.PluginID)
	if err != nil {
		return empty, nil, err
	}
	options, err := runtimeDeploymentOptions()
	if err != nil {
		return empty, nil, err
	}
	raw, _ := json.Marshal(options)
	if verify {
		if err = store.CheckConfiguration(ctx, c.PluginID); err != nil {
			return empty, nil, err
		}
		grant := pluginhost.Grant{Account: b.account, Plugin: i.Package.ID, Version: i.Package.Version, Permissions: i.Permissions}
		// This invocation checks installation/grants only; it does not prove
		// authorization by a conversation actor or bind message sources. Those
		// remain the calling installed external-tools policy's responsibility.
		invocation := pluginhost.Invocation{Account: b.account, Thread: "host:mcp", Requester: "administrator", Purpose: "use registered MCP runtime", RequestKey: "mcp-runtime-registration"}
		if err = pluginhost.Authorize(i.Package, grant, invocation, i.Package.Permissions); err != nil {
			return empty, nil, err
		}
		dir, err := store.PackageDirectory(i.Digest)
		if err != nil {
			return empty, nil, err
		}
		if _, err = pluginhost.Verify(dir); err != nil {
			return empty, nil, errors.New("mcp_runtime_package_integrity_changed")
		}
		manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err != nil || namedSpeechSHA(manifest) != i.Digest {
			return empty, nil, errors.New("mcp_runtime_package_integrity_changed")
		}
	}
	return mcpRuntimeBinding{Account: b.account, Installed: i, ConfigurationRevision: config.Revision, Deployment: namedSpeechSHA(raw), Authority: authority}, store, nil
}

func (b *mcpRuntimeBroker) check(ctx context.Context, c mcptools.ServerConfig, verify bool) (mcpRuntimeBinding, *pluginhost.Store, error) {
	current, store, err := b.read(ctx, c, verify)
	if err != nil {
		return current, nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if previous, ok := b.bindings[c.ID]; ok {
		if !reflect.DeepEqual(previous, current) {
			return current, nil, errors.New("mcp_runtime_registration_changed")
		}
	} else {
		b.bindings[c.ID] = current
	}
	return current, store, nil
}

func (b *mcpRuntimeBroker) guard(ctx context.Context, c mcptools.ServerConfig) error {
	if c.Transport != "stdio" {
		return nil
	}
	_, _, err := b.check(ctx, c, true)
	return err
}

func (b *mcpRuntimeBroker) launch(ctx context.Context, c mcptools.ServerConfig) (mcptools.StdioProcess, error) {
	binding, store, err := b.check(ctx, c, true)
	if err != nil {
		return nil, err
	}
	options, err := runtimeDeploymentOptions()
	if err != nil {
		return nil, err
	}
	installed := pluginruntime.InstalledExecutor(store, options)
	resolve := installed.Resolve
	installed.Resolve = func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginruntime.LaunchSpec, error) {
		spec, err := resolve(ctx, p, g)
		if err != nil {
			return spec, err
		}
		// Scope is read from the host context, hashed with the account; it is
		// never a tool-selected path, and cannot enlarge any existing mount.
		scope := namedSpeechSHA([]byte(b.account + "\x00" + mcptools.SessionScope(ctx)))
		spec.DataDir = filepath.Join(spec.DataDir, "mcp", scope)
		if err = os.MkdirAll(spec.DataDir, 0700); err != nil {
			return spec, err
		}
		return spec, nil
	}
	p := binding.Installed.Package
	g := pluginhost.Grant{Account: b.account, Plugin: p.ID, Version: p.Version, Permissions: binding.Installed.Permissions}
	stream, err := installed.StartStream(ctx, p, g)
	if err != nil {
		return nil, err
	}
	if _, _, err = b.check(ctx, c, true); err != nil {
		_ = stream.Close()
		return nil, err
	}
	owned := &mcpRuntimeStream{Stream: stream, stop: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-owned.stop:
				return
			case <-ticker.C:
				check, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _, err := b.check(check, c, false)
				cancel()
				if err != nil {
					_ = owned.Close()
					return
				}
			}
		}
	}()
	return owned, nil
}

type mcpRuntimeStream struct {
	*pluginruntime.Stream
	stop chan struct{}
	once sync.Once
}

func (s *mcpRuntimeStream) Close() error {
	s.once.Do(func() { close(s.stop) })
	return s.Stream.Close()
}
