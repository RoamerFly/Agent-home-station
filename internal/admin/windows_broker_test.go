package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"wechat-bot/internal/agenthost"
)

func TestDesktopBrokerDefaultPreservesPermissionsAndExplicitChoice(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows desktop transport default")
	}
	dir := t.TempDir()
	broker := agenthost.WindowsConfig{Pythonw: filepath.Join(dir, "pythonw.exe"), Helper: filepath.Join(dir, "broker.pyw"), Spool: filepath.Join(dir, "control")}
	b, _ := json.Marshal(broker)
	t.Setenv("WECHAT_WINDOWS_BROKER_CONFIG", string(b))
	a, err := newAgentControl(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings := a.config()
	if settings.Host.Windows == nil || settings.Host.Windows.Pythonw != broker.Pythonw || settings.Host.Mode != agentDefaults().Host.Mode || settings.Enabled != agentDefaults().Enabled {
		t.Fatal("default transport changed permissions")
	}
	explicit := broker
	explicit.Spool = filepath.Join(dir, "explicit-control")
	settings.Host.Windows = &explicit
	b, _ = json.Marshal(settings)
	if err = os.WriteFile(filepath.Join(dir, "agent-config.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_WINDOWS_BROKER_CONFIG", "invalid")
	a, err = newAgentControl(dir)
	if err != nil || a.config().Host.Windows.Spool != explicit.Spool {
		t.Fatal("explicit transport replaced", err)
	}
	if _, err = newAgentControl(t.TempDir()); err == nil {
		t.Fatal("invalid desktop default accepted")
	}
}
