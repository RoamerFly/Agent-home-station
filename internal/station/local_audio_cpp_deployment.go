package station

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"wechat-bot/internal/websession"
)

const localAudioCppDeploymentRoute = "/api/local/project-tools/audio-cpp/deployment"

// audioCppDeploymentRoutes registers only the host registration file. It does
// not copy binaries or models and never returns the submitted absolute path.
func (s *localProjectTools) audioCppDeploymentRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET "+localAudioCppDeploymentRoute, owner.Protect(s.audioCppDeployment))
	mux.HandleFunc("PUT "+localAudioCppDeploymentRoute, owner.Protect(s.audioCppDeployment))
}

func (s *localProjectTools) audioCppDeployment(w http.ResponseWriter, r *http.Request) {
	record, err := s.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		websession.Fail(w, http.StatusConflict, "请先准备本机主人身份")
		return
	}
	if r.Method == http.MethodPut {
		s.update.Lock()
		defer s.update.Unlock()
		var in struct {
			RegistrationPath string `json:"registration_path"`
			Approved         bool   `json:"approved"`
		}
		if !websession.Decode(w, r, &in) {
			return
		}
		if !in.Approved || !filepath.IsAbs(in.RegistrationPath) {
			websession.Fail(w, http.StatusBadRequest, "请确认并提供本机 audio.cpp 登记文件")
			return
		}
		if current, statusErr := s.audio.Info(r.Context(), record.FishID); statusErr == nil && current.Running {
			websession.Fail(w, http.StatusConflict, "请先停止正在运行的 audio.cpp 服务")
			return
		}
		data, readErr := os.ReadFile(filepath.Clean(in.RegistrationPath))
		if readErr != nil || len(data) == 0 || len(data) > localAudioCppMaxConfig {
			websession.Fail(w, http.StatusConflict, "audio.cpp 登记文件不可读")
			return
		}
		path := filepath.Join(s.home.dataDir, localAudioCppRegistrationFile)
		old, oldErr := os.ReadFile(path)
		tmp, writeErr := os.CreateTemp(s.home.dataDir, "audio-cpp-registration-")
		if writeErr != nil {
			websession.Fail(w, http.StatusServiceUnavailable, "audio.cpp 登记暂存目录不可用")
			return
		}
		tmpPath := tmp.Name()
		_, writeErr = tmp.Write(data)
		if closeErr := tmp.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr == nil {
			writeErr = os.Rename(tmpPath, path)
		}
		if writeErr != nil {
			_ = os.Remove(tmpPath)
			websession.Fail(w, http.StatusServiceUnavailable, "audio.cpp 登记文件无法保存")
			return
		}
		if _, checkErr := s.audio.Info(r.Context(), record.FishID); checkErr != nil {
			_ = os.Remove(path)
			if oldErr == nil {
				_ = os.WriteFile(path, old, 0600)
			}
			websession.Fail(w, http.StatusConflict, "audio.cpp 登记校验失败")
			return
		}
	}
	info, infoErr := s.audio.Info(r.Context(), record.FishID)
	result := map[string]any{"registered": infoErr == nil, "enabled": false, "running": false, "models": []localAudioCppModelView{}}
	if infoErr == nil {
		result["backend"], result["version"], result["commit"] = info.Backend, info.Version, info.Commit
		result["running"], result["models"] = info.Running, info.Models
		if host, hostErr := s.forFish(record.FishID); hostErr == nil {
			if item, itemErr := host.store.Installed(r.Context(), localAudioCppPackage); itemErr == nil {
				result["enabled"] = item.Enabled && !item.Removed
			}
		}
	}
	if infoErr != nil && !errors.Is(infoErr, context.Canceled) {
		result["reason"] = infoErr.Error()
	}
	websession.Respond(w, http.StatusOK, result)
}
