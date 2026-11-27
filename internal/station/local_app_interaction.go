package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/websession"
)

const localInteractionToolName = "app_action"

func localInteractionTool() mcptools.Tool {
	return mcptools.NewTool(localInteractionToolName, "管理已授权应用的点赞和评论回复。targets 查当前可创建的操作；create 绑定 content_id，点赞填 desired，回复填已读 comment_id 与 text。抖音评论通知到目标和直接父评论尚未逐条核实，通用路径暂不创建或提交抖音 reply_comment；原任务仍可 status/cancel。其他操作的 submit/resume 沿用原 job_id，list 查任务。结果待确认或需等 next_attempt_at 时沿用原 job_id。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["targets","create","submit","resume","status","cancel","list"]},"app_id":{"type":"string"},"connection_id":{"type":"string"},"content_id":{"type":"string"},"kind":{"type":"string","enum":["like","reply_comment"]},"comment_id":{"type":"string"},"text":{"type":"string"},"desired":{"type":"boolean"},"job_id":{"type":"string"},"after":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["action"],"additionalProperties":false}`)
}

type localInteractionInput struct {
	Action       string `json:"action"`
	AppID        string `json:"app_id"`
	ConnectionID string `json:"connection_id"`
	ContentID    string `json:"content_id"`
	Kind         string `json:"kind"`
	CommentID    string `json:"comment_id"`
	Text         string `json:"text"`
	Desired      *bool  `json:"desired"`
	JobID        string `json:"job_id"`
	After        int64  `json:"after"`
	Limit        int    `json:"limit"`
}

func (h *localChatService) interactionHost() (*apphost.Interactor, pluginhost.Invocation, error) {
	return h.interactionHostWithGuard(nil)
}

func (h *localChatService) interactionHostWithGuard(check func(context.Context, pluginhost.Job, apphost.InteractionDraft) error) (*apphost.Interactor, pluginhost.Invocation, error) {
	identity, err := h.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || h.stopping.Load() {
		return nil, pluginhost.Invocation{}, apphost.ErrInteractionUnavailable
	}
	actor := pluginhost.Invocation{Account: identity.FishID, Thread: identity.LocalOwner.ID, Requester: identity.LocalOwner.ID, Purpose: "local application interaction"}
	host, err := h.projectTools.forFish(identity.FishID)
	if err != nil {
		return nil, actor, err
	}
	validate := func(ctx context.Context, job pluginhost.Job, d apphost.InteractionDraft) error {
		if d.Target.AppID == douyinapp.ID && d.Kind == "reply_comment" {
			// Only the dedicated notice-bound origin carries the saved notification,
			// structured relation and completed work-context evidence. Old generic
			// jobs remain denied at create, submit and resume.
			if check != nil {
				if err := check(ctx, job, d); err != nil {
					return err
				}
			}
			return h.validateDouyinNoticeReply(ctx, job, d)
		}
		if check != nil {
			if err := check(ctx, job, d); err != nil {
				return err
			}
		}
		var origin struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(d.Origin, &origin) == nil && origin.Kind == "wechat_surf" {
			if h.wechat == nil {
				return apphost.ErrInteractionDenied
			}
			return h.wechat.validateSurfInteraction(ctx, job, d)
		}
		if h.validatePublication(ctx, job, apphost.PublicationDraft{Origin: d.Origin}) != nil {
			return apphost.ErrInteractionDenied
		}
		return nil
	}
	interactor := apphost.NewInteractor(h.applications, host.store, validate)
	if check != nil {
		interactor = interactor.WithQueryGuard(validate)
	}
	return interactor, actor, nil
}

func (h *localChatService) interactionTargets(ctx context.Context) []map[string]any {
	items := []map[string]any{}
	for _, app := range h.applications.Applications() {
		entry, _ := h.applications.Lookup(app.ID)
		provider, ok := entry.Provider.(apphost.InteractionProvider)
		if !ok {
			continue
		}
		connections, err := entry.Provider.Connections(ctx)
		if err != nil {
			continue
		}
		for _, c := range connections {
			ref := apphost.ConnectionRef{AppID: app.ID, InstallationID: entry.InstallationID, ConnectionID: c.ID, AccountID: c.AccountID}
			kinds := []string{}
			for _, kind := range []string{"like", "reply_comment"} {
				if app.ID == douyinapp.ID && kind == "reply_comment" {
					continue
				}
				if grant, err := provider.GrantInteraction(ctx, apphost.InteractionDraft{Target: ref, Kind: kind}); err == nil && grant.Allowed {
					kinds = append(kinds, kind)
				}
			}
			if len(kinds) > 0 {
				items = append(items, map[string]any{"target": ref, "kinds": kinds})
			}
		}
	}
	return items
}

func localInteractionAvailableView(view apphost.InteractionView) apphost.InteractionView {
	if view.Target.AppID != douyinapp.ID || view.Kind != "reply_comment" {
		return view
	}
	actions := make([]string, 0, len(view.AvailableActions))
	for _, action := range view.AvailableActions {
		if action != "submit" && action != "resume" {
			actions = append(actions, action)
		}
	}
	view.AvailableActions = actions
	return view
}

func localInteractionAvailableResult(result any) any {
	switch value := result.(type) {
	case apphost.InteractionView:
		return localInteractionAvailableView(value)
	case apphost.InteractionPage:
		for index := range value.Items {
			value.Items[index] = localInteractionAvailableView(value.Items[index])
		}
		return value
	default:
		return result
	}
}

func (t *localToolSet) callInteraction(ctx context.Context, raw json.RawMessage) (value string, failure error) {
	defer func() {
		if failure != nil {
			var safe interface{ SafeToolFailure() (string, string) }
			if !errors.As(failure, &safe) {
				status, message := localInteractionFailure(failure)
				failure = &localInteractionToolError{status: status, message: message}
			}
		}
	}()
	if t.publications == nil || t.run == nil {
		return "", apphost.ErrInteractionDenied
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	var in localInteractionInput
	if strictLocalMemoryDecode(raw, &in) != nil {
		return "", mcptools.ArgumentError("$", "应用操作参数无效")
	}
	host, actor, err := t.publications.interactionHost()
	if err != nil {
		return "", err
	}
	var result any
	switch in.Action {
	case "targets":
		result = map[string]any{"items": t.publications.interactionTargets(ctx)}
	case "list":
		if in.AppID == "" {
			in.AppID = "douyin"
		}
		if in.Limit == 0 {
			in.Limit = 20
		}
		result, err = host.List(ctx, actor, in.AppID, in.After, in.Limit)
	case "create":
		if t.inspectOnly {
			return "", mcptools.ArgumentError("$", "原操作结果待核对，请先查询原job_id")
		}
		if in.AppID == douyinapp.ID && in.Kind == "reply_comment" {
			return "", mcptools.ArgumentError("$.kind", "抖音通知尚未逐条核实目标评论及直接父评论；当前只能查看或取消原回复任务，不能经通用工具新建回复")
		}
		if in.JobID != "" || in.AppID == "" || in.ConnectionID == "" || in.ContentID == "" ||
			(in.Kind == "like" && (in.Desired == nil || in.CommentID != "" || in.Text != "")) ||
			(in.Kind == "reply_comment" && (in.Desired != nil || in.CommentID == "" || strings.TrimSpace(in.Text) == "")) {
			return "", mcptools.ArgumentError("$", "请明确操作类型、应用目标及内容；点赞需要desired，回复需要已读comment_id与正文")
		}
		draft := apphost.InteractionDraft{Kind: in.Kind, ContentID: in.ContentID, CommentID: in.CommentID, Text: in.Text}
		if in.Desired != nil {
			draft.Desired = *in.Desired
		}
		draft.Target, err = host.ResolveTarget(ctx, in.AppID, in.ConnectionID)
		if err != nil {
			return "", err
		}
		if in.Kind == "reply_comment" {
			entry, ok := t.publications.applications.Lookup(in.AppID)
			if !ok {
				return "", apphost.ErrInteractionUnavailable
			}
			resolver, ok := entry.Provider.(apphost.InteractionSubjectProvider)
			if !ok {
				return "", mcptools.ArgumentError("$", "该应用尚未提供可核对的评论回复目标")
			}
			draft.CommentVersion, err = resolver.ResolveInteractionSubject(ctx, draft)
			if err != nil {
				return "", mcptools.ArgumentError("$.comment_id", "请先读取原评论；可用app_observe(field=comments,refresh=true)更新，再核对回复权限，不能猜测编号")
			}
		}
		grant, e := t.access.resolve()
		if e != nil {
			return "", e
		}
		source, ok := t.artifactSource(grant)
		if !ok || source == nil {
			return "", apphost.ErrInteractionDenied
		}
		origin := t.publicationOrigin(source)
		draft.Origin, _ = json.Marshal(origin)
		actor.Authorization = localCheckpointKey(grant)
		actor.SourceVersions = map[string]int{source.EventID: source.Revision}
		actor.RequestKey = localCheckpointKey(localInteractionToolName, source.EventID, source.Revision, draft.Target, draft.Kind, draft.ContentID, draft.CommentID, draft.Text, draft.Desired, draft.CommentVersion)
		result, err = host.Create(ctx, actor, draft)
	case "submit", "resume", "status", "cancel":
		if !localDouyinPublicationHex(in.JobID) || in.AppID != "" || in.ConnectionID != "" || in.ContentID != "" || in.Kind != "" || in.CommentID != "" || in.Text != "" || in.Desired != nil {
			return "", mcptools.ArgumentError("$", "请只提供原job_id与操作，目标和正文以原任务为准")
		}
		if t.inspectOnly && (in.Action == "submit" || in.Action == "resume") {
			return "", mcptools.ArgumentError("$", "先核对原操作结果，不能新增点击或回复")
		}
		result, err = host.Action(ctx, actor, in.JobID, in.Action)
	default:
		return "", mcptools.ArgumentError("$", "请选择创建、提交、查询或取消应用操作")
	}
	if err != nil {
		status, message := localInteractionFailure(err)
		return "", &localInteractionToolError{status: status, message: message}
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	return localProcessJSON(localInteractionAvailableResult(result))
}

func (h *localChatService) interactionRoutes(s *websession.Store, mux *http.ServeMux) {
	handler := s.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		host, actor, err := h.interactionHost()
		if err != nil {
			code, msg := localInteractionFailure(err)
			websession.Fail(w, code, msg)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		stop := context.AfterFunc(h.ctx, cancel)
		defer stop()
		var result any
		if r.Method == http.MethodGet {
			app := r.URL.Query().Get("app_id")
			if app == "" {
				app = "douyin"
			}
			limit := 20
			after := int64(0)
			if value := r.URL.Query().Get("limit"); value != "" {
				limit, err = strconv.Atoi(value)
			}
			if value := r.URL.Query().Get("after"); value != "" {
				var e error
				after, e = strconv.ParseInt(value, 10, 64)
				if e != nil {
					err = e
				}
			}
			if err != nil || limit < 1 || after < 0 {
				websession.Fail(w, 400, "页大小或续读位置无效")
				return
			}
			var page apphost.InteractionPage
			page, err = host.List(ctx, actor, app, after, limit)
			result = map[string]any{"tasks": localInteractionAvailableResult(page), "targets": h.interactionTargets(ctx)}
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			var in struct {
				Action string `json:"action"`
				JobID  string `json:"job_id"`
			}
			if !websession.Decode(w, r, &in) {
				return
			}
			if !localDouyinPublicationHex(in.JobID) || (in.Action != "submit" && in.Action != "resume" && in.Action != "status" && in.Action != "cancel") {
				websession.Fail(w, 400, "请提供原任务编号与提交、查询或取消操作")
				return
			}
			result, err = host.Action(ctx, actor, in.JobID, in.Action)
		}
		if err != nil {
			code, msg := localInteractionFailure(err)
			websession.Fail(w, code, msg)
			return
		}
		websession.Respond(w, 200, localInteractionAvailableResult(result))
	})
	mux.HandleFunc("GET /api/local/interactions", handler)
	mux.HandleFunc("POST /api/local/interactions", handler)
}

func localInteractionFailure(err error) (int, string) {
	switch {
	case errors.Is(err, apphost.ErrInteractionDenied):
		return 403, "应用操作许可、原消息来源或所用资料版本已变化，请按原任务编号核对结果"
	case errors.Is(err, apphost.ErrInteractionOwnership):
		return 403, "这条应用操作不属于当前本机主人"
	case errors.Is(err, apphost.ErrInteractionInvalid):
		return 400, "应用目标、评论来源或操作参数无法核对"
	case errors.Is(err, pluginhost.ErrRequestConflict):
		return 409, "同一请求已有操作记录；请从list找到原job_id核对，不能改写旧任务重提"
	case errors.Is(err, apphost.ErrNotFound):
		return 404, "未找到原应用操作，请先查看list中的实际任务编号"
	case errors.Is(err, apphost.ErrInteractionConflict), errors.Is(err, pluginhost.ErrRevision):
		return 409, "任务状态已变化，请查询原任务继续"
	case errors.Is(err, apphost.ErrInteractionReceipt):
		return 409, "操作回执与原任务不一致，原任务保留待核对"
	case errors.Is(err, context.DeadlineExceeded):
		return 504, "应用操作请求超时，原任务保留待核对，请按原编号查询结果"
	case errors.Is(err, context.Canceled):
		return 408, "本次应用操作请求已中断，原任务保留待核对，请按原编号查询结果"
	case errors.Is(err, apphost.ErrInteractionUnavailable):
		return 503, "应用连接暂不可用，原任务保留，可按原编号核对"
	default:
		return 503, "应用操作暂时不可用，原任务保留，可按原编号核对"
	}
}

// Messages are selected by the host, never interpolated from a provider body.
type localInteractionToolError struct {
	status  int
	message string
}

func (e *localInteractionToolError) Error() string { return e.message }
func (e *localInteractionToolError) SafeToolFailure() (string, string) {
	return "application_interaction_" + strconv.Itoa(e.status), e.message
}
