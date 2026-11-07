package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/websession"
)

const localFollowupToolName = "local_task_followup"

func (h *localChatService) checkLocalTaskContinuation(ctx context.Context, store *companion.Store, in companion.Input, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	job, err := store.GetLocalTaskFollowup(ctx, in, id)
	if err != nil || job.State != "generating" || job.RequestID != in.TraceID || in.Source == nil || job.Source.EventID != in.Source.EventID || job.Source.Revision != in.Source.Revision || job.Source.BodyHash != in.Source.BodyHash {
		return errors.New("原任务已停止或来源已变化")
	}
	grant, err := (&localWorkspaceAccess{home: h.home, fishID: in.Account, ownerID: in.Sender}).resolve()
	if err != nil || grant.Revision != job.HomeRevision {
		return errors.New("原任务工作空间授权已变化")
	}
	return nil
}

func localFollowupTool() mcptools.Tool {
	return mcptools.NewTool(localFollowupToolName, "本机长程序结束后回看结果并继续原请求。schedule只接受本轮已实际启动的process_id，宿主绑定原来源，完成后自动核对结果并写回本机聊天；登记成功不是程序完成。status/cancel用原id，list按游标查看。cancel只停止自动续办，停止原程序请另用程序工具cancel；未知或中断结果不会重新启动程序。续办不增加应用权限，不用承诺代替登记。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["schedule","status","cancel","list"]},"process_id":{"type":"string"},"id":{"type":"string"},"after":{"type":"string"},"limit":{"type":"integer","minimum":1}},"required":["action"],"additionalProperties":false}`)
}

type localFollowupInput struct {
	Action    string `json:"action"`
	ProcessID string `json:"process_id"`
	ID        string `json:"id"`
	After     string `json:"after"`
	Limit     int    `json:"limit"`
}

func (t *localToolSet) callFollowup(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.publications == nil || t.run == nil || t.store == nil || t.access == nil {
		return "", errors.New("本机任务续办不可用")
	}
	if t.run.CheckSource(ctx) != nil {
		return "", errors.New("原请求来源已变化，未安排续办")
	}
	var args localFollowupInput
	if strictLocalMemoryDecode(raw, &args) != nil {
		return "", errors.New("本机续办参数无效")
	}
	_, in, err := t.publications.conversation()
	if err != nil || in.Account != t.access.fishID || in.Sender != t.access.ownerID {
		return "", errors.New("本机任务身份已变化")
	}
	in.TraceID = t.traceID
	var value any
	switch args.Action {
	case "schedule":
		if args.ID != "" || args.After != "" || args.Limit != 0 || t.inspectOnly || !t.run.Tracked {
			return "", errors.New("续办请核对原计划，新计划须来自新的有效执行回合")
		}
		grant, err := t.access.resolve()
		if err != nil {
			return "", errors.New("本机工作空间授权不可用")
		}
		source, ok := t.artifactSource(grant)
		if !ok || source == nil || !followupOwnsProcess(t, grant, args.ProcessID) {
			return "", errors.New("该程序不属于本轮已核验的启动操作，未安排续办")
		}
		value, err = t.store.ScheduleLocalTaskFollowup(ctx, in, *source, args.ProcessID, grant.Revision, time.Now().UTC())
		if err != nil {
			return "", errors.New("续办计划未能保存，请先核对原任务")
		}
		t.publications.followups.notify()
	case "list":
		if args.ID != "" || args.ProcessID != "" {
			return "", errors.New("列出续办只接受游标和页大小")
		}
		if args.Limit == 0 {
			args.Limit = 20
		}
		value, err = t.store.ListLocalTaskFollowups(ctx, in, args.After, args.Limit)
	case "status", "cancel":
		if args.ProcessID != "" || args.After != "" || args.Limit != 0 {
			return "", errors.New("查询或停止续办只使用原计划编号")
		}
		value, err = t.publications.localFollowupAction(ctx, t.store, in, args.ID, args.Action)
	default:
		return "", errors.New("请选择安排、查询、停止或列出续办")
	}
	if err != nil {
		return "", errors.New("续办计划不可用或状态已变化，请查询原编号")
	}
	if t.run.CheckSource(ctx) != nil {
		return "", errors.New("原请求来源已变化，本次结果未返回")
	}
	return localProcessJSON(value)
}

func followupOwnsProcess(tools *localToolSet, grant localWorkspaceGrant, id string) bool {
	if id == "" || tools.process == nil || tools.process.jobs == nil {
		return false
	}
	scope := localProcessScope(grant.FishID, grant.OwnerID)
	for _, op := range tools.run.Operations {
		if op.Tool == localProcessTool && op.Grant == scope {
			if p, ok := tools.process.jobs.FindByKey(scope, "local-"+op.Key); ok && p.ID == id {
				return true
			}
		}
	}
	return false
}

func (h *localChatService) localFollowupAction(ctx context.Context, store *companion.Store, in companion.Input, id, action string) (companion.LocalTaskFollowup, error) {
	job, err := store.GetLocalTaskFollowup(ctx, in, id)
	if err != nil || action == "status" {
		return job, err
	}
	if action != "cancel" {
		return job, errors.New("invalid followup action")
	}
	if job.State == "waiting" || job.State == "ready" || job.State == "generating" {
		job, err = store.UpdateLocalTaskFollowup(ctx, in, id, job.Revision, "cancelled", time.Now().UTC(), "owner_cancelled")
		if err == nil {
			h.followups.cancelCurrent(id)
		}
	}
	return job, err
}

func (h *localChatService) localFollowupRoutes(owner *websession.Store, mux *http.ServeMux) {
	for _, method := range []string{"GET", "POST"} {
		mux.HandleFunc(method+" /api/local/task-followups", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			store, in, err := h.conversation()
			if err != nil {
				websession.Fail(w, 409, "本机任务身份不可用")
				return
			}
			if r.Method == http.MethodGet {
				limit := 20
				if value := r.URL.Query().Get("limit"); value != "" {
					limit, err = strconv.Atoi(value)
					if err != nil || limit < 1 {
						websession.Fail(w, 400, "页大小应为正整数")
						return
					}
				}
				page, err := store.ListLocalTaskFollowups(r.Context(), in, r.URL.Query().Get("after"), limit)
				if err != nil {
					websession.Fail(w, 503, "续办记录无法读取")
					return
				}
				websession.Respond(w, 200, page)
				return
			}
			var input struct {
				Action string `json:"action"`
				ID     string `json:"id"`
			}
			if !websession.Decode(w, r, &input) {
				return
			}
			job, err := h.localFollowupAction(r.Context(), store, in, input.ID, input.Action)
			if err != nil {
				websession.Fail(w, 409, "续办计划不可用或状态已变化")
				return
			}
			websession.Respond(w, 200, job)
		}))
	}
}

type localTaskFollowupWorker struct {
	service     *localChatService
	wake        chan struct{}
	done        chan struct{}
	started     atomic.Bool
	mu          sync.Mutex
	current     string
	cancel      context.CancelFunc
	currentDone chan struct{}
}

func (w *localTaskFollowupWorker) interrupt() <-chan struct{} {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		w.cancel()
	}
	return w.currentDone
}

func (w *localTaskFollowupWorker) notify() {
	if w != nil {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}
func (w *localTaskFollowupWorker) cancelCurrent(id string) {
	if w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.current == id && w.cancel != nil {
			w.cancel()
		}
	}
}

func (h *localChatService) startLocalTaskFollowups() {
	w := h.followups
	if w == nil || !w.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-h.ctx.Done():
				return
			case <-ticker.C:
			case <-w.wake:
			}
			w.tick(h.ctx)
		}
	}()
}

func (w *localTaskFollowupWorker) tick(ctx context.Context) {
	h := w.service
	if ctx.Err() != nil || h.stopping.Load() || !h.turn.TryLock() {
		return
	}
	defer func() {
		h.turn.Unlock()
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.currentDone != nil {
			close(w.currentDone)
			w.currentDone = nil
		}
	}()
	identity, err := h.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		return
	}
	store, in, err := h.conversation()
	if err != nil {
		return
	}
	jobs, err := store.DueLocalTaskFollowups(ctx, in, time.Now().UTC(), 1)
	if err != nil {
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil || h.stopping.Load() {
			return
		}
		w.advance(ctx, store, in, job)
	}
}

func (w *localTaskFollowupWorker) advance(ctx context.Context, store *companion.Store, in companion.Input, job companion.LocalTaskFollowup) {
	h := w.service
	update := func(state, reason string, next time.Time) {
		_, _ = store.UpdateLocalTaskFollowup(context.WithoutCancel(ctx), in, job.ID, job.Revision, state, next, reason)
	}
	prior, err := store.LocalConversationRequest(ctx, in, job.RequestID)
	if err != nil || prior == nil {
		update("cancelled", "source_unavailable", time.Now().UTC())
		return
	}
	in.TraceID, in.Source, in.OriginalText = job.RequestID, &job.Source, &prior.InputText
	in.Text, in.MessageTime = prior.InputText, prior.OccurredAt
	if done, err := store.LocalContinuationAccepted(ctx, in, job.ID); err == nil && done {
		update("completed", "reply_saved", time.Now().UTC())
		return
	}
	if job.State == "generating" {
		// A crash inside the model call is uncertain. Query the durable reply
		// group first; never repeat the model or program merely after a restart.
		update("failed", "reply_interrupted_check_original", time.Now().UTC())
		return
	}
	run, err := store.OpenLocalToolRun(ctx, in, false)
	if err != nil || run.CheckSource(ctx) != nil {
		update("cancelled", "source_unavailable", time.Now().UTC())
		return
	}
	tools := h.tools(store, in)
	tools.run = run
	grant, err := tools.access.resolve()
	if err != nil || grant.Revision != job.HomeRevision || !followupOwnsProcess(tools, grant, job.ProcessID) {
		update("cancelled", "authorization_changed", time.Now().UTC())
		return
	}
	process, err := tools.process.jobs.Status(localProcessScope(grant.FishID, grant.OwnerID), job.ProcessID, false)
	if err != nil {
		update("failed", "process_record_unavailable", time.Now().UTC())
		return
	}
	if process.State == "running" {
		update("waiting", "waiting_for_process", time.Now().UTC().Add(2*time.Second))
		return
	}
	if process.State == "cancelled" {
		update("cancelled", "process_cancelled", time.Now().UTC())
		return
	}
	if job.State == "waiting" {
		job, err = store.UpdateLocalTaskFollowup(ctx, in, job.ID, job.Revision, "ready", time.Now().UTC(), "process_result_available")
		if err != nil {
			return
		}
	}
	job, err = store.UpdateLocalTaskFollowup(ctx, in, job.ID, job.Revision, "generating", time.Now().UTC().Add(3*time.Minute), "reading_process_result")
	if err != nil {
		return
	}
	continuation, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.current, w.cancel = job.ID, cancel
	w.currentDone = make(chan struct{})
	w.mu.Unlock()
	defer func() { cancel(); w.mu.Lock(); w.current, w.cancel = "", nil; w.mu.Unlock() }()
	h.busy.Store(true)
	defer h.busy.Store(false)
	result, _ := json.Marshal(map[string]any{"id": process.ID, "state": process.State, "output": process.Output, "exit_code": process.ExitCode, "truncated": process.Truncated, "reason": process.Reason, "updated": process.Updated})
	in.BackgroundResult, in.HistoryObserved, in.TaskID, in.OriginTraceID = true, true, job.ID, job.RequestID
	in.Text = "这是原本机任务的新结果，原请求见 source_user_request。依据实际结果完成回看，不重启程序、不重复已完成操作；没有新内容时可安静。"
	in.RecoveryToolResults = []companion.RecoveryToolResult{{Tool: localProcessTool, CallKey: job.ID, Result: string(result), ResultID: process.ID, OccurredAt: process.Updated.UTC().Format(time.RFC3339Nano), ReadOnly: true}}
	_, err = h.generateLocalTurn(continuation, store, in, run, job.ID)
	state, reason := "completed", "result_reviewed"
	if err != nil {
		state, reason = "failed", "result_review_failed"
		var safe interface{ SafeToolFailure() (string, string) }
		if errors.As(err, &safe) {
			reason, _ = safe.SafeToolFailure()
		}
	}
	if latest, readErr := store.GetLocalTaskFollowup(context.WithoutCancel(ctx), in, job.ID); readErr == nil && latest.State == "generating" {
		_, _ = store.UpdateLocalTaskFollowup(context.WithoutCancel(ctx), in, job.ID, latest.Revision, state, time.Now().UTC(), reason)
	}
}
