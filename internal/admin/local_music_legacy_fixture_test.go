package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// installLegacyLocalMusicPlugin upgrades synthetic legacy test configuration
// into an installed official package. Only the isolated process boundary is
// simulated: Controller, call authority, HTTP, validation and private results
// use the production path, and classification runs the installed Python code.
func installLegacyLocalMusicPlugin(t *testing.T, m *Manager) legacyLocalMusicExecutor {
	t.Helper()
	store, err := m.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), "../../plugins/music")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	legacy := readLegacySpeechTestConfig(t, m)
	endpoint, _ := json.Marshal(legacy.YuEEndpoint)
	if _, err = store.SaveConfiguration(t.Context(), "music", pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"local_endpoint": endpoint}, Secrets: map[string]string{"local_token": legacy.YuEToken}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(t.Context(), "music", installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	dir, err := store.PackageDirectory(installed.Digest)
	if err != nil {
		t.Fatal(err)
	}
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		local, _ := filepath.Abs("../../dist/windows-native/runtimes/windows-python-cli/python.exe")
		if _, statErr := os.Stat(local); statErr == nil {
			python = local
		} else {
			python, err = exec.LookPath("python")
		}
	}
	if err != nil {
		t.Fatal("official local music classifier requires Python", err)
	}
	executor := legacyLocalMusicExecutor{python: python, module: filepath.Join(dir, "runtime", "local_music.py")}
	controller, err := pluginruntime.NewController(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	m.packageMu.Lock()
	m.packageRuntime, m.packageRuntimeReady = controller, true
	m.packageMu.Unlock()
	return executor
}

type legacyLocalMusicExecutor struct{ python, module string }

func (e legacyLocalMusicExecutor) Start(_ context.Context, p pluginhost.Package, _ pluginhost.Grant) (pluginhost.Session, error) {
	if p.ID != "music" {
		return nil, errors.New("legacy music fixture only executes the official music package")
	}
	return e, nil
}

func (legacyLocalMusicExecutor) Health(ctx context.Context) error { return ctx.Err() }
func (legacyLocalMusicExecutor) Close() error                     { return nil }

func (e legacyLocalMusicExecutor) Call(ctx context.Context, _ pluginhost.Invocation, tool string, args json.RawMessage) (json.RawMessage, error) {
	if tool == musicPreparationCapability {
		const prepare = `import importlib.util,json,sys
sys.stdin.reconfigure(encoding="utf8")
sys.stdout.reconfigure(encoding="utf8")
spec=importlib.util.spec_from_file_location("official_music_preparation",sys.argv[1])
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
try:
 result=module.prepare(json.load(sys.stdin))
except ValueError as error:
 result={"ok":False,"error":str(error)}
json.dump(result,sys.stdout)`
		cmd := exec.CommandContext(ctx, e.python, "-I", "-B", "-c", prepare, filepath.Join(filepath.Dir(e.module), "preparation.py"))
		cmd.Stdin = bytes.NewReader(args)
		result, err := cmd.Output()
		if err != nil {
			return nil, errors.New("official music preparation fixture failed")
		}
		return json.RawMessage(result), nil
	}
	if tool != localMusicCapability {
		return nil, errors.New("legacy music fixture only executes the local bridge capability")
	}
	reply, err := pluginruntime.InvokeHostService(ctx, "wechat/music_bridge", args)
	if err != nil {
		return nil, err
	}
	input, err := json.Marshal([]json.RawMessage{args, reply})
	if err != nil {
		return nil, err
	}
	const classify = `import importlib.util,json,sys
spec=importlib.util.spec_from_file_location("official_local_music",sys.argv[1])
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
args,reply=json.load(sys.stdin)
json.dump(module.bridge_request(args,lambda _:reply),sys.stdout)`
	cmd := exec.CommandContext(ctx, e.python, "-I", "-B", "-c", classify, e.module)
	cmd.Stdin = bytes.NewReader(input)
	result, err := cmd.Output()
	if err != nil {
		return nil, errors.New("official local music fixture classification failed")
	}
	return json.RawMessage(result), nil
}

func legacyLocalMusicAdminContext(t *testing.T, m *Manager) context.Context {
	t.Helper()
	return context.WithValue(t.Context(), speechAdminTestContextKey{}, &speechAdminTestAuthority{
		Account: m.native.Account(), Session: "synthetic-local-admin", RequestID: t.Name(),
		Check: func(ctx context.Context) error { return ctx.Err() },
	})
}
