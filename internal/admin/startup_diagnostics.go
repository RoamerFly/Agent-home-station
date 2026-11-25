package admin

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const startupDiagnosticsMaxBytes = 2 << 20

type startupDiagnosticFailure struct {
	At      time.Time `json:"at"`
	Stage   string    `json:"stage"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
	LogFile string    `json:"log_file"`
}

type startupDiagnostics struct {
	Available         bool                      `json:"available"`
	LogDirectory      string                    `json:"log_directory,omitempty"`
	StartupLog        string                    `json:"startup_log,omitempty"`
	NativeLog         string                    `json:"native_log,omitempty"`
	LatestEventAt     time.Time                 `json:"latest_event_at,omitzero"`
	LatestStage       string                    `json:"latest_stage,omitempty"`
	LatestStatus      string                    `json:"latest_status,omitempty"`
	LastSuccessAt     time.Time                 `json:"last_success_at,omitzero"`
	LastFailure       *startupDiagnosticFailure `json:"last_failure,omitempty"`
	FailureHistorical bool                      `json:"failure_historical"`
	LogIsHistorical   bool                      `json:"log_is_historical"`
}

func (m *Manager) startupLogDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("WECHAT_STARTUP_LOG_DIR")); configured != "" {
		return filepath.Clean(configured)
	}
	// The Windows release keeps the admin data under data/applications/wechat.
	// Keep this derived fallback for existing packages and test fixtures.
	data := filepath.Dir(filepath.Dir(filepath.Dir(m.dir)))
	return filepath.Join(data, "main_setting", "logs")
}

func (m *Manager) startupDiagnostics() startupDiagnostics {
	dir := m.startupLogDirectory()
	out := startupDiagnostics{
		LogDirectory: dir,
		StartupLog:   filepath.Join(dir, "startup-stage.log"),
		NativeLog:    filepath.Join(dir, "wechat-native.log"),
	}
	latest := time.Time{}
	if at, stage, status, failure, ok := readStartupStageLog(out.StartupLog); ok {
		out.Available = true
		out.LatestEventAt, out.LatestStage, out.LatestStatus = at, stage, status
		latest = at
		if failure != nil {
			out.LastFailure = failure
		}
		if stage == "server.admitted" || stage == "server.start.ok" {
			out.LastSuccessAt = at
		}
	}
	if at, failure, ok := readNativeFailure(out.NativeLog); ok {
		out.Available = true
		if at.After(latest) {
			out.LatestEventAt = at
			latest = at
		}
		if out.LastFailure == nil || !failure.At.Before(out.LastFailure.At) {
			out.LastFailure = failure
		}
	}
	if !latest.IsZero() {
		out.LogIsHistorical = time.Since(latest) > 30*time.Minute
	}
	if out.LastFailure != nil && !out.LastSuccessAt.IsZero() && !out.LastFailure.At.After(out.LastSuccessAt) {
		out.FailureHistorical = true
	}
	return out
}

func readStartupStageLog(path string) (time.Time, string, string, *startupDiagnosticFailure, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, "", "", nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > startupDiagnosticsMaxBytes {
		return time.Time{}, "", "", nil, false
	}
	var latest time.Time
	var stage, status string
	var failure *startupDiagnosticFailure
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			parts = strings.SplitN(line, " ", 2)
		}
		if len(parts) != 2 {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		event := strings.TrimSpace(parts[1])
		latest, stage, status = at, event, stageStatus(event)
		if event == "server.start.failed" {
			failure = &startupDiagnosticFailure{At: at, Stage: "station_server", Code: event, Message: "工作站服务启动失败", LogFile: path}
		}
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, "", "", nil, false
	}
	return latest, stage, status, failure, !latest.IsZero()
}

func readNativeFailure(path string) (time.Time, *startupDiagnosticFailure, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > startupDiagnosticsMaxBytes {
		return time.Time{}, nil, false
	}
	var latest time.Time
	var failure *startupDiagnosticFailure
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.SplitN(strings.TrimSpace(scanner.Text()), " ", 2)
		if len(parts) != 2 {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			continue
		}
		if at.After(latest) {
			latest = at
		}
		var entry struct {
			Event string `json:"event"`
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(parts[1]), &entry) != nil || entry.Event != "native_start_failed" {
			continue
		}
		failure = &startupDiagnosticFailure{At: at, Stage: "wechat_native", Code: "native_start_failed", Message: entry.Error, LogFile: path}
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, nil, false
	}
	return latest, failure, !latest.IsZero()
}

func stageStatus(event string) string {
	switch event {
	case "server.start.ok", "server.admitted", "wechat.start.ok", "legacy-admin.start.ok":
		return "ok"
	case "server.start.failed":
		return "failed"
	default:
		return "running"
	}
}
