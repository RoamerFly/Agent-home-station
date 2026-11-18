package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"wechat-bot/internal/mcptools"
)

// The same lifecycle implementation serves desktop controls and Agent calls.
func (s *localProjectTools) changeLifecycle(ctx context.Context, host *localProjectToolHost, id string, revision int, action string, idleOnly ...bool) error {
	if action == "stop" || action == "restart" {
		var err error
		idle := len(idleOnly) > 0 && idleOnly[0]
		if idle {
			pending, e := host.store.HasUnfinishedJobs(ctx, id)
			if e != nil {
				return e
			}
			if pending || s.isActive(id) || host.controller.Status(id).ActiveCalls > 0 {
				return errors.New("工具正在处理任务，请完成后再停止或重启")
			}
		}
		switch id {
		case localRVCPackage:
			if s.rvc != nil {
				err = s.rvc.Stop(ctx)
			}
		case localYuEPackage:
			if s.yue != nil {
				err = s.yue.Stop(ctx)
			}
		case localComfyPackage:
			if s.comfy != nil {
				err = s.comfy.Stop(ctx)
			}
		case localAudioCppPackage:
			if s.audio != nil {
				err = s.audio.Stop(ctx)
			}
		case localTTSPackage:
			if s.ttsCancel != nil {
				s.ttsCancel()
			}
		case localOBSPackage:
			err = s.stopOBSProcess(ctx)
		default:
			if idle {
				err = host.controller.StopIdleSession(ctx, id, revision)
			} else {
				err = host.controller.StopSession(ctx, id, revision)
			}
		}
		if err != nil || action == "stop" {
			return err
		}
	}
	fish := host.store.Account()
	switch id {
	case localRVCPackage:
		if s.rvc == nil {
			return errors.New("RVC 后端未登记")
		}
		info, err := s.rvc.Info(ctx, fish)
		if err != nil {
			return err
		}
		if len(info.Profiles) == 0 {
			return errors.New("RVC 声线未登记")
		}
		_, _, err = s.rvc.Call(ctx, fish, info.Binding, http.MethodGet, "/rvc-health?voice_profile_id="+url.QueryEscape(info.Profiles[0].ID), "", nil)
		return err
	case localYuEPackage:
		if s.yue == nil {
			return errors.New("YuE 后端未登记")
		}
		info, err := s.yue.Info(ctx, fish)
		if err != nil {
			return err
		}
		return s.yue.Use(ctx, fish, info.Binding, true, func(localComfyAudioClient) error { return nil })
	case localComfyPackage:
		if s.comfy == nil {
			return errors.New("ComfyUI 后端未登记")
		}
		info, _, err := s.comfySetup(ctx, fish)
		if err != nil {
			return err
		}
		return s.comfy.Use(ctx, fish, info.Binding, true, func(localComfyClient) error { return nil })
	case localAudioCppPackage:
		if s.audio == nil {
			return errors.New("audio.cpp 后端未登记")
		}
		return s.audio.Start(ctx, fish)
	case localTTSPackage:
		if s.tts == nil {
			return errors.New("语音后端未登记")
		}
		info, err := s.tts.Info(ctx, fish)
		if err != nil {
			return err
		}
		if len(info.Voices) == 0 {
			return errors.New("语音声线未登记")
		}
		return nil // one-shot synthesis: no idle process to invent
	case localOBSPackage:
		view, err := localOBSConfiguration(ctx, host)
		if err != nil {
			return err
		}
		if !view.Configured {
			return errors.New("OBS 预览未配置")
		}
		config, err := host.store.ConfigurationForExecution(ctx, id)
		if err != nil {
			return err
		}
		var password string
		if json.Unmarshal(config.Values["password"], &password) != nil {
			return errors.New("OBS 凭据不可读")
		}
		if err := s.ensureOBSProcess(ctx, view.Port, password); err != nil {
			return err
		}
		if s.obs.status(view).Active {
			return nil
		}
		session, err := s.obs.claim("start", s.obs.ticket())
		if err != nil {
			return err
		}
		frame, err := s.obs.read(ctx, session, view.Port, password, false)
		_, err = s.obs.finish(session, view, frame, err)
		return err
	default:
		return host.controller.Start(ctx, id, revision)
	}
}

func stationLifecycleTools() []mcptools.Tool {
	out := []mcptools.Tool{}
	for _, action := range []string{"status", "enable", "start", "stop", "restart"} {
		schema := `{"type":"object","properties":{"tool_id":{"type":"string"}},"additionalProperties":false}`
		if action != "status" {
			schema = `{"type":"object","properties":{"tool_id":{"type":"string"}},"required":["tool_id"],"additionalProperties":false}`
		}
		out = append(out, mcptools.NewTool("station_tool_"+action, "工作站公共工具生命周期："+action+"。status 列出工具和未就绪原因；enable 使主人已授权安装的外部工具按需待命；启动、停止和重启用于已启用工具。按次执行的工具启动时检查就绪状态。", schema))
	}
	return out
}

func (t *localToolSet) callStationLifecycle(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if t.projectTools == nil || t.inspectOnly {
		return "", errors.New("工作站工具管理不可用")
	}
	if _, err := t.access.resolve(); err != nil {
		return "", err
	}
	var in struct {
		ToolID string `json:"tool_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", err
	}
	s := t.projectTools
	record, err := s.home.identity.Read()
	if err != nil {
		return "", err
	}
	host, err := s.forFish(record.FishID)
	if err != nil {
		return "", err
	}
	action := strings.TrimPrefix(name, "station_tool_")
	views := []localProjectToolView{}
	sources := discoverToolPackages(s.options.CatalogDirectory)
	for _, p := range s.packages(ctx, host, sources) {
		if in.ToolID != "" && in.ToolID != p.ID {
			continue
		}
		v, err := s.projectView(ctx, host, p.ID, p.Name, sources)
		if err != nil {
			return "", err
		}
		views = append(views, v)
	}
	if action == "status" {
		out, _ := json.Marshal(map[string]any{"tools": views})
		return string(out), nil
	}
	if action != "enable" && action != "start" && action != "stop" && action != "restart" {
		return "", errors.New("未知工具操作")
	}
	if in.ToolID == "" || len(views) != 1 {
		return "", errors.New("工具未登记，请先查看状态")
	}
	if action == "enable" && (!views[0].Installed || !views[0].ExternalPackage) {
		return "", errors.New("请由主人在项目工具中安装并授权该工具")
	}
	payload, _ := json.Marshal(map[string]any{"action": action, "revision": views[0].Revision})
	request := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.changeProjectTool(response, request, views[0].ID, views[0].Name)
	if response.Code != 200 {
		return "", errors.New(strings.TrimSpace(response.Body.String()))
	}
	return response.Body.String(), nil
}
