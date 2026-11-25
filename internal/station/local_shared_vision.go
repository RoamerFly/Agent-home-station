package station

import (
	"io"
	"net/http"
	"strings"
	"wechat-bot/internal/strictjson"

	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/websession"
)

// Application hosts call this after checking the source and conversation.
// The provider key remains inside the workbench's media tool host.
func (s *localProjectTools) sharedVisionObserve(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 29<<20)
	var in struct {
		Source string           `json:"source"`
		Prompt string           `json:"prompt"`
		Images []deepseek.Image `json:"images"`
		Tokens int              `json:"tokens"`
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		websession.Fail(w, 415, "请求需要使用 JSON")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || strictjson.DecodeKnownFields(body, &in) != nil {
		websession.Fail(w, 400, "视觉请求格式无效或过大")
		return
	}
	text, err := s.home.observeLocalStationImages(r.Context(), in.Source, in.Prompt, in.Images, in.Tokens, r.Context().Err)
	if err != nil {
		websession.Fail(w, 409, "工作站视觉服务未返回有效结果，请检查公共媒体识别配置。")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, 200, map[string]string{"text": text})
}
