package admin

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestPortableRuntimeOptionsUseExeSiblingEnvRuntime(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows package layout only")
	}
	executable := filepath.FromSlash(`E:/station/env/windows-x64/wechat-admin.exe`)
	options := portableRuntimeOptions(executable)
	if options.Python != filepath.FromSlash(`E:/station/env/windows-python-cli/python.exe`) || options.Pythonw != filepath.FromSlash(`E:/station/env/windows-python-cli/pythonw.exe`) {
		t.Fatalf("unexpected python runtime paths: %+v", options)
	}
	if options.WindowsHelper != filepath.FromSlash(`E:/station/env/windows-agent-host/windows_plugin_host.pyw`) {
		t.Fatalf("unexpected helper path: %s", options.WindowsHelper)
	}
	if options.CatalogDirectory != filepath.FromSlash(`E:/station/publictools`) {
		t.Fatalf("unexpected catalog path: %s", options.CatalogDirectory)
	}
}
