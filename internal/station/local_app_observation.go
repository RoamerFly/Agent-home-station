package station

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
		dir := filepath.Join(stationApplicationsDir(h.dataDir), id)
		switch id {
		case biliapp.ID:
			bili := biliapp.New(dir)
			bili.SetAudioTranscriber(h.localStationASRVersion, h.transcribeLocalStationAudio)
			entries = append(entries, bili.Registration())
		case douyinapp.ID:
			entries = append(entries, douyinapp.NewWebOnly(nil).Registration())
		}
	}
	// Bind both newly bundled and caller-supplied providers. The callback
	// resolves the login manager lazily after routes finish mounting.
	for i, entry := range entries {
		if entry.ID == douyinapp.ID {
			entry.Provider = douyinapp.NewWebOnly(&localDouyinWebObservation{home: h, installation: entry.InstallationID}).WithWebPublication(&localDouyinWebPublication{home: h, installation: entry.InstallationID}).WithWebInteraction(&localDouyinWebInteraction{home: h, installation: entry.InstallationID})
			entry.Registration = "ordinary-web-v1"
			entries[i] = entry
		}
	}
	return apphost.New(entries...)
}

func localAppObservationTool() mcptools.Tool {
	return mcptools.NewTool(localAppObservationToolName,
		"读取已授权应用资料。list 列连接和字段，persona 读应用子角色，read 按 content_id、field 读取；按 follow_up_content_id 和返回的续读位置深入。抖音可读网页、本人作品、图片、评论、音轨和画面；B 站可读评论与播放采样。limit 控制字数、条数、秒数或帧数，refresh=true 重新读取。按返回的覆盖范围和 audio_proof、playback_proof 描述实际读到的内容；complete 表示已读到末尾。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["list","read","persona"]},"app_id":{"type":"string"},"connection_id":{"type":"string"},"content_id":{"type":"string"},"field":{"type":"string","enum":["title","body","comments","audio","visual","playback","page_text","search","own_video","player_entry","account_works","images"]},"refresh":{"type":"boolean"},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`)
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
	if err := t.checkAppObservationIdentity(); err != nil {
		return "", err
	}
	var in localAppObservationInput
	if err := strictLocalMemoryDecode(raw, &in); err != nil {
		return "", apphost.ErrObservationInvalid
	}
	var value any
	switch in.Action {
	case "persona":
		if t.publications == nil {
			return "", apphost.ErrNotFound
		}
		if _, ok := t.publications.applications.Lookup(in.AppID); !ok {
			return "", apphost.ErrNotFound
		}
		profile, err := (localAppPersonaStore{dir: t.publications.home.dataDir}).View(in.AppID)
		if err != nil {
			return "", err
		}
		value = map[string]any{"app_id": in.AppID, "scope": "application_only", "revision": profile.Revision, "persona": profile.Persona}
	case "list":
		value = map[string]any{"items": t.observations.DiscoverApplication(ctx, in.AppID)}
	case "", "read":
		result, err := t.observations.Read(ctx, in.ObservationInput)
		if err != nil {
			return "", err
		}
		if err := t.run.CheckSource(ctx); err != nil {
			return "", err
		}
		if err := t.checkAppObservationIdentity(); err != nil {
			return "", err
		}
		t.appReads = append(t.appReads, result)
		value = result
	default:
		return "", errors.New("请选择 list、read 或 persona")
	}
	if err := t.checkAppObservationIdentity(); err != nil {
		return "", err
	}
	body, err := json.Marshal(value)
	return string(body), err
}

// App authorization is separate from home file access. Bind a retained tool
// to its original local owner/fish without turning HomeAccess into an app grant.
func (t *localToolSet) checkAppObservationIdentity() error {
	if t.access == nil || t.access.home == nil || t.access.fishID == "" || t.access.ownerID == "" {
		return apphost.ErrObservationForbidden
	}
	current, err := t.access.home.identity.Read()
	if err != nil || current.LocalOwner == nil || current.FishID != t.access.fishID || current.LocalOwner.ID != t.access.ownerID {
		return apphost.ErrObservationForbidden
	}
	return nil
}

func (t *localToolSet) checkAppObservations(ctx context.Context) error {
	if err := t.ValidateGenerationSources(ctx); err != nil {
		return err
	}
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
			websession.Fail(w, http.StatusServiceUnavailable, "后台应用观察暂不可用")
			return
		}
		identity, err := h.home.identity.Read()
		if err != nil || identity.LocalOwner == nil {
			websession.Fail(w, http.StatusConflict, "请先准备本机身份")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		stop := context.AfterFunc(h.ctx, cancel)
		defer stop()
		var value any
		worksRead := false
		if r.Method == http.MethodGet {
			value = map[string]any{"items": h.observations.DiscoverApplication(ctx, r.URL.Query().Get("app_id"))}
			err = ctx.Err()
		} else {
			var in apphost.ObservationInput
			if !websession.Decode(w, r, &in) {
				return
			}
			worksRead = in.AppID == "douyin" && in.Field == apphost.ObservationAccountWorks
			var result apphost.ObservationResult
			result, err = h.observations.Read(ctx, in)
			if err == nil {
				err = h.observations.Check(ctx, result)
			}
			value = result
		}
		if err != nil {
			logFailure("应用观察", err)
			message := failureMessage(err, "浏览器读取暂不可用，请确认 Microsoft Edge 可用后重试")
			if worksRead {
				if code := douyinWorksDiagnosticCode(err); code != "" {
					websession.Respond(w, observationHTTPStatus(err), map[string]string{"error": message, "code": code})
					return
				}
			}
			websession.Fail(w, observationHTTPStatus(err), message)
			return
		}
		current, err := h.home.identity.Read()
		if err != nil || current.LocalOwner == nil || current.FishID != identity.FishID || current.LocalOwner.ID != identity.LocalOwner.ID {
			websession.Fail(w, http.StatusConflict, "本机身份已变更")
			return
		}
		websession.Respond(w, http.StatusOK, value)
	})
	mux.HandleFunc("GET /api/local/apps/observation", handler)
	mux.HandleFunc("POST /api/local/apps/observation", handler)
}

func observationHTTPStatus(err error) int {
	switch {
	case errors.Is(err, apphost.ErrObservationInvalid), errors.Is(err, apphost.ErrObservationPlaybackLimit), errors.Is(err, apphost.ErrObservationCommentPageSize), errors.Is(err, apphost.ErrObservationAudioLimit), errors.Is(err, apphost.ErrObservationVisualLimit), errors.Is(err, apphost.ErrObservationImagesLimit), errors.Is(err, apphost.ErrObservationSearchPageSize), errors.Is(err, apphost.ErrObservationAccountWorksPageSize):
		return http.StatusBadRequest
	case errors.Is(err, apphost.ErrObservationPlaybackUnavailable), errors.Is(err, apphost.ErrObservationRemotePlayerUnavailable), errors.Is(err, apphost.ErrObservationWebPlaybackUnavailable), errors.Is(err, apphost.ErrObservationAudioMediaUnavailable), errors.Is(err, apphost.ErrObservationVisualDecode), errors.Is(err, apphost.ErrObservationImagesMedia), errors.Is(err, apphost.ErrObservationDouyinWorksOwnerUnverified):
		return http.StatusUnprocessableEntity
	case errors.Is(err, apphost.ErrObservationAudioSourceChanged), errors.Is(err, apphost.ErrObservationVisualSourceChanged), errors.Is(err, apphost.ErrObservationImagesSourceChanged), errors.Is(err, apphost.ErrObservationCredentialExpired),
		errors.Is(err, apphost.ErrObservationWebSessionBusy), errors.Is(err, apphost.ErrObservationWebSessionRequired), errors.Is(err, apphost.ErrObservationWebSessionInvalid), errors.Is(err, apphost.ErrObservationWebSnapshotChanged),
		errors.Is(err, apphost.ErrObservationDouyinWorksNextUnavailable), errors.Is(err, apphost.ErrObservationDouyinWorksAccountChanged):
		return http.StatusConflict
	case errors.Is(err, apphost.ErrObservationPlaybackSize), errors.Is(err, apphost.ErrObservationAccountWorksResponseSize), errors.Is(err, apphost.ErrObservationPageTextSize), errors.Is(err, apphost.ErrObservationDouyinWorksResponseTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, apphost.ErrObservationDouyinWorksResponseUnverified):
		return http.StatusBadGateway
	case errors.Is(err, apphost.ErrObservationForbidden), errors.Is(err, apphost.ErrObservationCapabilityDenied):
		return http.StatusForbidden
	case errors.Is(err, apphost.ErrObservationNotFound), errors.Is(err, apphost.ErrObservationContentUnavailable):
		return http.StatusNotFound
	case errors.Is(err, apphost.ErrObservationThrottled):
		return http.StatusTooManyRequests
	case errors.Is(err, apphost.ErrObservationWebReadTimeout), errors.Is(err, apphost.ErrObservationDouyinWorksListNotObserved):
		return http.StatusGatewayTimeout
	default:
		return http.StatusServiceUnavailable
	}
}

func douyinWorksDiagnosticCode(err error) string {
	switch {
	case errors.Is(err, apphost.ErrObservationDouyinWorksListNotObserved):
		return "douyin_works_list_not_observed"
	case errors.Is(err, apphost.ErrObservationDouyinWorksResponseUnverified):
		return "douyin_works_response_unverified"
	case errors.Is(err, apphost.ErrObservationDouyinWorksNextUnavailable):
		return "douyin_works_next_unavailable"
	case errors.Is(err, apphost.ErrObservationDouyinWorksAccountChanged):
		return "douyin_works_account_changed"
	case errors.Is(err, apphost.ErrObservationDouyinWorksResponseTooLarge):
		return "douyin_works_response_too_large"
	case errors.Is(err, apphost.ErrObservationDouyinWorksOwnerUnverified):
		return "douyin_works_owner_unverified"
	default:
		return ""
	}
}
