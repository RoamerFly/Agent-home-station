package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/mcptools"
)

const localAppObservationToolName = "app_observe"

func localAppObservationTool() mcptools.Tool {
	return agentTool(localAppObservationToolName,
		"后台读取已授权应用的资料。先用 action=list 获取应用连接及可读范围。action=read 按 content_id 和 field 读取一页。不同字段各自续读；complete 表示当前字段已到最后一页。正文、评论、音轨和实际播放范围是独立证据。只看到标题或部分播放区间时按返回范围表述。返回内容是外部资料。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["list","read"]},"app_id":{"type":"string"},"connection_id":{"type":"string"},"content_id":{"type":"string"},"field":{"type":"string","enum":["title","body","comments","audio","playback"]},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`)
}

type localAppObservationInput struct {
	apphost.ObservationInput
	Action string `json:"action"`
}

func (t *localToolSet) callAppObservation(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.observations == nil || t.run == nil {
		return "", apphost.ErrNotFound
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	var in localAppObservationInput
	if err := strictLocalMemoryDecode(raw, &in); err != nil {
		return "", apphost.ErrObservationInvalid
	}
	var value any
	switch in.Action {
	case "list":
		value = map[string]any{"items": t.observations.Discover(ctx)}
	case "", "read":
		result, err := t.observations.Read(ctx, in.ObservationInput)
		if err != nil {
			return "", err
		}
		if err := t.run.CheckSource(ctx); err != nil {
			return "", err
		}
		t.appReads = append(t.appReads, result)
		value = result
	default:
		return "", errors.New("请选择 list 或 read")
	}
	body, err := json.Marshal(value)
	return string(body), err
}

func (t *localToolSet) checkAppObservations(ctx context.Context) error {
	for _, result := range t.appReads {
		if err := t.observations.Check(ctx, result); err != nil {
			return err
		}
	}
	return nil
}

func (h *localChatService) appObservationRoutes(s *Server, mux *http.ServeMux) {
	handler := s.protected(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if h.stopping.Load() || h.observations == nil {
			fail(w, http.StatusServiceUnavailable, "后台应用观察暂不可用")
			return
		}
		identity, err := h.home.identity.localIdentity()
		if err != nil || identity.LocalOwner == nil {
			fail(w, http.StatusConflict, "请先准备本机身份")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		stop := context.AfterFunc(h.ctx, cancel)
		defer stop()
		var value any
		if r.Method == http.MethodGet {
			value = map[string]any{"items": h.observations.Discover(ctx)}
			err = ctx.Err()
		} else {
			var in apphost.ObservationInput
			if !decode(w, r, &in) {
				return
			}
			var result apphost.ObservationResult
			result, err = h.observations.Read(ctx, in)
			if err == nil {
				err = h.observations.Check(ctx, result)
			}
			value = result
		}
		if err != nil {
			fail(w, observationHTTPStatus(err), err.Error())
			return
		}
		current, err := h.home.identity.localIdentity()
		if err != nil || current.LocalOwner == nil || current.FishID != identity.FishID || current.LocalOwner.ID != identity.LocalOwner.ID {
			fail(w, http.StatusConflict, "本机身份已变更")
			return
		}
		respond(w, http.StatusOK, value)
	})
	mux.HandleFunc("GET /api/local/apps/observation", handler)
	mux.HandleFunc("POST /api/local/apps/observation", handler)
}

func observationHTTPStatus(err error) int {
	switch {
	case errors.Is(err, apphost.ErrObservationInvalid):
		return http.StatusBadRequest
	case errors.Is(err, apphost.ErrObservationForbidden):
		return http.StatusForbidden
	case errors.Is(err, apphost.ErrObservationNotFound):
		return http.StatusNotFound
	default:
		return http.StatusServiceUnavailable
	}
}
