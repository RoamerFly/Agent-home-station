package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"wechat-bot/internal/localasr"
	"wechat-bot/internal/pluginhost"
)

func (s *localProjectTools) populateToolBackends(ctx context.Context, host *localProjectToolHost, view *localProjectToolView) {
	local := pluginhost.ToolBackendView{Name: "本机后端", Kind: "local", State: "missing"}
	if view.RuntimeReady && view.Installed {
		local.State = "configured"
	}
	switch view.ID {
	case localasr.PluginID:
		asr := pluginhost.ToolBackendView{Name: "语音与音轨识别", Kind: "local", State: "missing", Scope: "工作站"}
		vision := pluginhost.ToolBackendView{Name: "视觉观察", Kind: "api", State: "missing", Credential: "api_key", Scope: "工作站"}
		if host != nil && view.Installed {
			if info, err := s.home.localAudioCppASR(ctx); err == nil {
				asr.State, asr.Model = "configured", "audio.cpp / "+info.ASRModelID
			} else if binding, err := s.home.localStationASRBinding(ctx); err == nil {
				asr.State, asr.Model = "configured", binding.profile.Metadata().ProfileID
			}
			if v, err := s.localVisionView(ctx, host); err == nil {
				vision.SetEndpoint(v.Configuration.Endpoint)
				vision.Model, vision.KeyConfigured = v.Configuration.Model, v.KeyConfigured
				if validateLocalVisionConfiguration(v.Configuration) == nil && v.KeyConfigured {
					vision.State = "configured"
				}
			} else {
				vision.State = "unreadable"
			}
		}
		view.Backends = []pluginhost.ToolBackendView{asr, vision}
	case localOBSPackage:
		local.Name, local.Kind, local.Credential = "OBS WebSocket", "loopback", "local_password"
		if host != nil {
			if config, err := localOBSConfiguration(ctx, host); err == nil && config.Configured {
				local.Origin = fmt.Sprintf("ws://127.0.0.1:%d", config.Port)
				local.KeyConfigured = true
			}
		}
		view.Backends = []pluginhost.ToolBackendView{local}
	case localAudioPackage, localRVCPackage, localComfyPackage, localYuEPackage, localTTSPackage, localAudioCppPackage, "document-workshop", "project-development":
		view.Backends = []pluginhost.ToolBackendView{local}
	case "web-search":
		search := pluginhost.ToolBackendView{Name: "公共搜索", Kind: "web", State: local.State}
		if host != nil && view.Installed {
			if config, err := host.store.Configuration(ctx, view.ID); err == nil {
				var backend string
				_ = json.Unmarshal(config.Values["search_backend"], &backend)
				if backend == "deepseek" {
					search.Name, search.Kind, search.Credential = "DeepSeek 搜索", "api", "api_key"
					search.SetEndpoint("https://api.deepseek.com/v1/tools/search")
					search.KeyConfigured = slices.Contains(config.ConfiguredSecrets, "search_key")
					if !search.KeyConfigured {
						search.State = "missing"
					}
				}
			} else {
				search.State = "unreadable"
			}
		}
		view.Backends = []pluginhost.ToolBackendView{search}
	case "share-reader":
		view.Backends = []pluginhost.ToolBackendView{{Name: "公开链接读取", Kind: "web", State: local.State}}
	case localExternalPackage:
		state := "missing"
		if raw, err := s.externalRegistration(); err == nil {
			if _, bridge, err := externalHTTPRegistration(raw); err != nil {
				state = "unreadable"
			} else if len(bridge.Protocol().Tools) != 0 {
				state = "configured"
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			state = "unreadable"
		}
		view.Backends = []pluginhost.ToolBackendView{{Name: "同步 HTTP 连接", Kind: "api", State: state, Scope: "工作站"}}
		if view.Installed && view.Enabled && view.RuntimeReady && state != "configured" && view.Reason == "" {
			view.Reason = "请在工作站私有登记目录配置同步 HTTP 连接。"
		}
	case "speech":
		view.Backends = []pluginhost.ToolBackendView{{Name: view.Name, Kind: "package", State: "declared"}}
	default:
		if view.ExternalPackage {
			if host != nil && view.Installed {
				view.Backends = declaredToolBackends(ctx, host.store, view.ID)
			}
			if len(view.Backends) == 0 {
				view.Backends = []pluginhost.ToolBackendView{{Name: "外部工具包", Kind: "package", State: "declared"}}
			}
		}
	}
}

// Only routes explicitly declared to the host have a known connection type.
// A package with its own internal configuration is described as such instead.
func declaredToolBackends(ctx context.Context, store *pluginhost.Store, id string) []pluginhost.ToolBackendView {
	installed, err := store.Installed(ctx, id)
	if err != nil || len(installed.Package.HTTPRoutes) == 0 {
		return nil
	}
	config, err := store.Configuration(ctx, id)
	if err != nil {
		return nil
	}
	schema, err := store.ConfigSchema(ctx, id)
	if err != nil {
		return nil
	}
	out := make([]pluginhost.ToolBackendView, 0, len(installed.Package.HTTPRoutes))
	for _, route := range installed.Package.HTTPRoutes {
		b := pluginhost.ToolBackendView{Name: route.ID, Kind: "api", State: "missing"}
		endpoint := route.Endpoint
		if route.EndpointConfig != "" {
			raw := config.Values[route.EndpointConfig]
			if len(raw) == 0 {
				raw = schema.Properties[route.EndpointConfig].Default
			}
			_ = json.Unmarshal(raw, &endpoint)
		}
		b.SetEndpoint(endpoint)
		if route.Credential != "" {
			b.Credential = "api_key"
			if route.CredentialHeader == "Cookie" {
				b.Credential = "session"
			}
			b.KeyConfigured = slices.Contains(config.ConfiguredSecrets, route.Credential)
		}
		if b.Origin != "" && (b.Credential == "" || b.KeyConfigured) {
			b.State = "configured"
		}
		out = append(out, b)
	}
	return out
}
