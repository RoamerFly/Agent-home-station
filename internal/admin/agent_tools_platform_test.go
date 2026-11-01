package admin

import (
	"path/filepath"
	"testing"
	"wechat-bot/internal/agenthost"
)

func TestWindowsToolCatalogueOmitsUnsupportedLinuxExecutor(t *testing.T) {
	f, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	c := f.manager.agent.config()
	c.Enabled, c.Host.Mode = true, "full"
	c.Host.Windows = &agenthost.WindowsConfig{Pythonw: filepath.Join(c.Host.Roots[0], "pythonw.exe"), Helper: filepath.Join(c.Host.Roots[0], "helper.pyw"), Spool: filepath.Join(c.Host.Roots[0], "private")}
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range p.agentTools() {
		names[tool.Name] = true
	}
	if !names["server_windows_process"] || !names["server_list"] || names["server_exec"] || names["server_process"] {
		t.Fatalf("wrong Windows tools: %+v", names)
	}
	if p.agentToolEnabled("server_exec") {
		t.Fatal("unsupported Linux execution accepted on Windows")
	}
}
