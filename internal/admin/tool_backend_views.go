package admin

import (
	"context"
	"encoding/json"
	"slices"

	"wechat-bot/internal/pluginhost"
)

func (m *Manager) toolBackendViews(ctx context.Context, store *pluginhost.Store, id string) []pluginhost.ToolBackendView {
	switch id {
	case "media-understanding", "share-reader", "music", "web-search":
	default:
		return nil
	}
	c, err := store.Configuration(ctx, id)
	if err != nil {
		return []pluginhost.ToolBackendView{{Name: "工具配置", Kind: "unknown", State: "unreadable"}}
	}
	value := func(key string) string { var out string; _ = json.Unmarshal(c.Values[key], &out); return out }
	api := func(name, endpoint, model, key string) pluginhost.ToolBackendView {
		b := pluginhost.ToolBackendView{Name: name, Kind: "api", State: "missing", Model: model, Credential: "api_key", KeyConfigured: slices.Contains(c.ConfiguredSecrets, key)}
		b.SetEndpoint(endpoint)
		if b.Origin != "" && b.KeyConfigured {
			b.State = "configured"
		}
		return b
	}
	switch id {
	case "media-understanding":
		asr := api("语音与视频音轨", value("asr_endpoint"), value("asr_model"), "asr_key")
		if value("asr_backend") == "local" {
			asr = pluginhost.ToolBackendView{Name: asr.Name, Kind: "local", State: "missing"}
			if ready, _ := m.localASRStatus()["available"].(bool); ready {
				asr.State = "configured"
				asr.Model = value("local_asr_profile_id")
			}
		} else if asr.Model == "" {
			asr.State = "missing"
		}
		vision := api("视频画面", value("vision_endpoint"), value("vision_model"), "vision_key")
		if vision.Model == "" {
			vision.State = "missing"
		}
		return []pluginhost.ToolBackendView{asr, vision}
	case "web-search":
		if value("search_backend") == "deepseek" {
			return []pluginhost.ToolBackendView{api("DeepSeek 搜索", "https://api.deepseek.com/v1/tools/search", "", "search_key")}
		}
		return []pluginhost.ToolBackendView{{Name: "公共搜索与网页读取", Kind: "web", State: "configured"}}
	case "music":
		b := pluginhost.ToolBackendView{Name: "本地音乐服务", Kind: "service", State: "missing", Credential: "local_token", KeyConfigured: slices.Contains(c.ConfiguredSecrets, "local_token")}
		b.SetEndpoint(value("local_endpoint"))
		if b.Origin != "" && b.KeyConfigured {
			b.State = "configured"
		}
		return []pluginhost.ToolBackendView{b}
	case "share-reader":
		out := []pluginhost.ToolBackendView{{Name: "公开链接读取", Kind: "web", State: "configured"}}
		for _, site := range []struct{ name, key string }{{"视频号助手", "assistant_cookie"}, {"元宝", "yuanbao_cookie"}} {
			b := pluginhost.ToolBackendView{Name: site.name, Kind: "web", State: "missing", Credential: "session", KeyConfigured: slices.Contains(c.ConfiguredSecrets, site.key)}
			if b.KeyConfigured {
				b.State = "configured"
			}
			out = append(out, b)
		}
		return out
	}
	return nil
}
