package station

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wechat-bot/internal/obspreview"
	"wechat-bot/internal/pluginruntime"
)

type localOBSDeployment struct {
	Executable string `json:"executable"`
	SHA256     string `json:"sha256"`
}

// Optional owner registration controls a separate portable preview instance.
// The fixed launch arguments never request recording, streaming or capture.
func (s *localProjectTools) ensureOBSProcess(ctx context.Context, port int, password string) (result error) {
	path := stationToolRegistrationPath(s.home.dataDir, "station-obs-preview.json")
	raw, portable, err := portableRegistration(path, s.options.CatalogDirectory, localOBSPackage, s.home.root, 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil // Existing externally managed OBS remains supported.
	}
	if err != nil {
		return err
	}
	var r localOBSDeployment
	if localComfyStrictDecode(raw, &r) != nil || !strings.EqualFold(filepath.Base(r.Executable), "obs64.exe") || !localComfyHexDigest.MatchString(r.SHA256) {
		return errors.New("OBS 独立预览登记无效")
	}
	profile := "StationPreview"
	portableConfig := ""
	if portable != "" {
		if err := resolvePortablePaths(portable, &r.Executable); err != nil {
			return err
		}
		r, err = s.preparePortableOBSPayload(r, port, password)
		if err != nil {
			return err
		}
		profile = "Station"
		portableConfig = filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(r.Executable))), "config")
	}
	if !filepath.IsAbs(r.Executable) {
		return errors.New("OBS 独立预览登记无效")
	}
	check := func(context.Context) error {
		if !localComfySafeRegular(r.Executable) || !localComfyFileMatches(r.Executable, r.SHA256) {
			return errors.New("OBS 程序已变化，请重新登记")
		}
		studio := filepath.Dir(filepath.Dir(filepath.Dir(r.Executable)))
		actual, e := os.Stat(filepath.Join(studio, "config"))
		expected := filepath.Join(stationToolDataDir(s.home.dataDir, "obs-preview"), "obs-preview", "config")
		if portableConfig != "" {
			expected = portableConfig
		}
		want, expectedErr := os.Stat(expected)
		if e != nil || expectedErr != nil || !os.SameFile(actual, want) {
			return errors.New("OBS 独立预览配置目录不匹配")
		}
		return nil
	}
	defer func() {
		if result != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_ = s.obsProcesses.Stop(stopCtx, "preview")
		}
	}()
	logs := filepath.Join(stationToolDataDir(s.home.dataDir, "obs-preview"), "obs-preview")
	if err = os.MkdirAll(logs, 0700); err != nil {
		return err
	}
	_, err = s.obsProcesses.Start(ctx, "preview", pluginruntime.ManagedServiceSpec{OwnerPlugin: localOBSPackage, Binding: r.SHA256, Executable: r.Executable, Directory: filepath.Dir(r.Executable), Arguments: []string{"--portable", "--multi", "--websocket_ipv4_only", "--only-bundled-plugins", "--disable-updater", "--disable-missing-files-check", "--minimize-to-tray", "--profile", profile, "--collection", profile}, Environment: os.Environ(), LogPath: filepath.Join(logs, "process.log"), StopTimeout: 5 * time.Second, Check: check}, func(_ context.Context, start func() error) error { return start() })
	if err != nil {
		return err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if pid, ok := s.obsProcesses.ProcessID("preview"); ok && localOBSListenerOwner(port, pid) == nil {
			// The WebSocket listener comes up before OBS loads its scene collection.
			// Readiness needs an actual frame, not merely an open port.
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, probeErr := obspreview.Capture(probeCtx, port, password)
			cancel()
			if probeErr == nil {
				return nil
			}
			if errors.Is(probeErr, obspreview.ErrAuthentication) {
				return probeErr
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("OBS 独立预览启动超时")
		case <-ticker.C:
		}
	}
}
func (s *localProjectTools) stopOBSProcess(ctx context.Context) error {
	s.obs.stop()
	return s.obsProcesses.Stop(ctx, "preview")
}
