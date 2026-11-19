package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupDiagnosticsReadsLatestFailureAndMarksHistoricalLogs(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "data", "main_setting", "logs")
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "startup-stage.log"), []byte("2026-11-18T14:35:41.8856851Z\tserver.start.failed\n2026-11-18T14:38:09.3036481Z\tserver.admitted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "wechat-native.log"), []byte("2026-11-18T14:35:41.8856851Z {\"event\":\"native_start_failed\",\"error\":\"manifest mismatch\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{dir: filepath.Join(dir, "data", "applications", "wechat", "legacy-admin")}
	d := m.startupDiagnostics()
	if !d.Available || d.LastFailure == nil || d.LastFailure.Stage != "wechat_native" {
		t.Fatalf("unexpected diagnostics: %+v failure=%+v", d, d.LastFailure)
	}
	if d.LatestStage != "server.admitted" || d.LatestStatus != "ok" || !d.LogIsHistorical || !d.FailureHistorical {
		t.Fatalf("unexpected latest state: %+v", d)
	}
}
