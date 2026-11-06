package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	biliapp "wechat-bot/connected-apps/bilibili/adapter"
	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/mcptools"

	"wechat-bot/internal/websession"
)

const localAppObservationToolName = "app_observe"

func (h *localHomeService) bindBundledObservationApps(registry apphost.Registry) (apphost.Registry, error) {
	applications := registry.Applications()
	entries := make([]apphost.Registration, 0, len(applications)+2)
	for _, app := range applications {
		entry, _ := registry.Lookup(app.ID)
		entries = append(entries, entry)
	}
	for _, id := range []string{biliapp.ID, douyinapp.ID} {
		if _, registered := registry.Lookup(id); registered {
			continue
		}
		if _, _, err := h.verifyAppSource(id); err != nil {
			continue
		}
		dir := filepath.Join(h.dataDir, "apps", id)
		switch id {
		case biliapp.ID:
			bili := biliapp.New(dir)
			bili.SetAudioTranscriber(h.localBilibiliAudioCallbacks())
			entries = append(entries, bili.Registration())
		case douyinapp.ID:
			entries = append(entries, douyinapp.New(dir).Registration())
		}
	}
	if len(entries) == len(applications) {
		return registry, nil
	}
	return apphost.New(entries...)
}

func localAppObservationTool() mcptools.Tool {
	return agentTool(localAppObservationToolName,
		"后台读取已授权应用的资料。用 action=list 查看连接及可用字段，action=read 按 content_id 和 field 读取。page_text 是网页文字。抖音 search 读取主人已授权关键词的官方结果，可续页；refresh=true 从首页重新搜索。搜索结果若带 follow_up_content_id，可把它作为 content_id，使用同一连接的 follow_up_field=player_entry 核对官方播放器入口。该入口仅确认来源。若入口返回 follow_up_field=playback，使用它的跟进编号做抖音独立静音采样，limit 为秒数，默认 6，可选 1–20。采样仅证明实际播放区间。抖音 own_video 核对指定作品的当前状态。account_works 读取主人在账号连接中获准的作品列表，每轮最多四页，续读省略 limit，refresh=true 从首页读取。列表不说明作品是否公开。B 站 comments 是公开一级评论，默认每页 10 条，可选 1–30；续读时省略 limit。B 站 audio 的 limit 为本地解码窗口秒数，默认 60，可选 1–240。读取 audio_proof 区分已转写、未识别到词和无音轨，续读从上一窗口末尾继续。B 站 playback 的 limit 为静音采样秒数，默认 6，可选 1–20。按 playback 区间表述实际播放。只有 playback_proof.fully_played 为 true 才表述为完整播放。complete 表示本次读取没有后续页。返回内容是外部资料。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["list","read"]},"app_id":{"type":"string"},"connection_id":{"type":"string"},"content_id":{"type":"string"},"field":{"type":"string","enum":["title","body","comments","audio","playback","page_text","search","own_video","player_entry","account_works"]},"refresh":{"type":"boolean"},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`)
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

func (h *localChatService) appObservationRoutes(s *websession.Store, mux *http.ServeMux) {
	handler := s.Protect(func(w http.ResponseWriter, r *http.Request) {
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
	case errors.Is(err, apphost.ErrObservationInvalid), errors.Is(err, apphost.ErrObservationPlaybackLimit), errors.Is(err, apphost.ErrObservationCommentPageSize), errors.Is(err, apphost.ErrObservationAudioLimit), errors.Is(err, apphost.ErrObservationSearchPageSize), errors.Is(err, apphost.ErrObservationAccountWorksPageSize):
		return http.StatusBadRequest
	case errors.Is(err, apphost.ErrObservationPlaybackUnavailable), errors.Is(err, apphost.ErrObservationRemotePlayerUnavailable), errors.Is(err, apphost.ErrObservationAudioMediaUnavailable):
		return http.StatusUnprocessableEntity
	case errors.Is(err, apphost.ErrObservationAudioSourceChanged), errors.Is(err, apphost.ErrObservationCredentialExpired):
		return http.StatusConflict
	case errors.Is(err, apphost.ErrObservationPlaybackSize), errors.Is(err, apphost.ErrObservationAccountWorksResponseSize):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, apphost.ErrObservationForbidden), errors.Is(err, apphost.ErrObservationCapabilityDenied):
		return http.StatusForbidden
	case errors.Is(err, apphost.ErrObservationNotFound), errors.Is(err, apphost.ErrObservationContentUnavailable):
		return http.StatusNotFound
	case errors.Is(err, apphost.ErrObservationThrottled):
		return http.StatusTooManyRequests
	default:
		return http.StatusServiceUnavailable
	}
}
