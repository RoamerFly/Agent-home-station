package station

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/websession"
)

func (s *localProjectTools) publicToolBalance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	if id != "media-understanding" && id != "web-search" {
		websession.Fail(w, 404, "该工具没有余额查询接口")
		return
	}
	record, err := s.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		websession.Fail(w, 409, "本机身份暂不可读")
		return
	}
	host, err := s.forFish(record.FishID)
	if err != nil {
		websession.Fail(w, 409, "本机工具状态不可读")
		return
	}
	config, err := host.store.ConfigurationForExecution(r.Context(), id)
	if err != nil {
		websession.Fail(w, 409, "请先完成公共工具配置并启用工具")
		return
	}
	endpoint, model, key := balanceCredentials(id, config)
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "api.deepseek.com" || u.User != nil || key == "" {
		websession.Fail(w, 409, "此服务未提供可用的模型 Key 余额接口")
		return
	}
	client, err := deepseek.NewWithAPIKey(deepseek.Config{BaseURL: "https://api.deepseek.com", Model: model}, key)
	if err != nil {
		websession.Fail(w, 409, "余额查询凭据不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := client.Balance(ctx)
	if err != nil {
		websession.Fail(w, 502, "余额查询暂不可用")
		return
	}
	websession.Respond(w, 200, result)
}

func balanceCredentials(id string, configuration pluginhost.RuntimeConfiguration) (endpoint, model, key string) {
	value := func(field string) string { var v string; _ = json.Unmarshal(configuration.Values[field], &v); return v }
	if id == "web-search" {
		if value("search_backend") == "deepseek" {
			return "https://api.deepseek.com", "deepseek-flash", value("search_key")
		}
		return "", "", ""
	}
	return value("vision_endpoint"), value("vision_model"), value("vision_key")
}
