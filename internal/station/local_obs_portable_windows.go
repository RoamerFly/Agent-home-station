//go:build windows

package station

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"wechat-bot/internal/obsportable"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

const portableOBSPort = 14457

func (s *localProjectTools) configurePortableOBS(ctx context.Context, host *localProjectToolHost) error {
	view, err := localOBSConfiguration(ctx, host)
	if err != nil || view.ConfigRevision != 0 || view.Configured {
		return err
	}
	_, portable, err := portableRegistration(stationToolRegistrationPath(s.home.dataDir, "station-obs-preview.json"), s.options.CatalogDirectory, localOBSPackage, s.home.root, 16<<10)
	if errors.Is(err, os.ErrNotExist) || portable == "" {
		return nil
	}
	if err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	_, err = host.store.SaveConfiguration(ctx, localOBSPackage, pluginhost.ConfigUpdate{
		Revision: view.ConfigRevision,
		Values:   map[string]json.RawMessage{"port": json.RawMessage(strconv.Itoa(portableOBSPort))},
		Secrets:  map[string]string{"password": hex.EncodeToString(secret)},
	})
	return err
}

func (s *localProjectTools) preparePortableOBSPayload(r localOBSDeployment, port int, password string) (localOBSDeployment, error) {
	if !localComfySafeRegular(r.Executable) || !localComfyFileMatches(r.Executable, r.SHA256) {
		return r, errors.New("便携 OBS 程序校验失败")
	}
	source := filepath.Dir(filepath.Dir(filepath.Dir(r.Executable)))
	if filepath.Base(source) != "studio" {
		return r, errors.New("便携 OBS 程序目录无效")
	}
	private := filepath.Join(stationToolRoot(s.home.dataDir, "obs-preview"), "runtimes")
	parent, err := workspacefs.EnsurePath(private)
	if err != nil {
		return r, err
	}
	defer parent.Close()
	destination := filepath.Join(private, r.SHA256)
	if _, err := parent.Lstat(r.SHA256); errors.Is(err, os.ErrNotExist) {
		staging, err := os.MkdirTemp(private, ".studio-")
		if err != nil {
			return r, err
		}
		defer parent.RemoveAll(filepath.Base(staging))
		// Only the shipped distribution directories enter the private runtime.
		// Configuration and profiles from a source machine never enter it.
		for _, name := range []string{"bin", "data", "obs-plugins"} {
			from := filepath.Join(source, name)
			if !localRVCSafeDirectory(from) {
				return r, errors.New("便携 OBS 缺少运行组件")
			}
			if err := os.CopyFS(filepath.Join(staging, name), os.DirFS(from)); err != nil {
				return r, err
			}
		}
		if err := parent.Rename(filepath.Base(staging), r.SHA256); err != nil {
			return r, err
		}
	} else if err != nil {
		return r, err
	}
	root, err := workspacefs.OpenPath(destination)
	if err != nil {
		return r, err
	}
	defer root.Close()
	r.Executable = filepath.Join(destination, "bin", "64bit", "obs64.exe")
	if !localComfyFileMatches(r.Executable, r.SHA256) {
		return r, errors.New("OBS 预览实例程序校验失败")
	}
	if _, running := s.obsProcesses.ProcessID("preview"); !running {
		if err := obsportable.WritePreviewProfile(root, port, password); err != nil {
			return r, err
		}
	}
	return r, nil
}
