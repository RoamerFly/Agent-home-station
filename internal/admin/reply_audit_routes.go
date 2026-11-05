package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func (s *Server) replyAuditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent/runtime-capabilities", s.protected(func(w http.ResponseWriter, r *http.Request) {
		result, err := s.manager.InspectRegisteredCapabilities(r.Context())
		if err != nil {
			fail(w, 500, "能力登记暂不可读")
			return
		}
		respond(w, 200, result)
	}))
	mux.HandleFunc("GET /api/reply-audits", s.protected(func(w http.ResponseWriter, r *http.Request) {
		thread := r.URL.Query().Get("thread_id")
		if thread == "" {
			fail(w, 400, "请选择会话")
			return
		}
		scope := companion.ScopeID("wechat", s.manager.native.Account(), thread)
		s.replyAuditList(w, r, scope)
	}))
	mux.HandleFunc("GET /api/memory/{scope}/reply-audits", s.protected(func(w http.ResponseWriter, r *http.Request) {
		s.replyAuditList(w, r, r.PathValue("scope"))
	}))
	mux.HandleFunc("GET /api/memory/{scope}/reply-audits/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		scope := r.PathValue("scope")
		if _, err := s.manager.memoryScope(scope); err != nil {
			fail(w, 404, "会话不存在")
			return
		}
		services, err := s.manager.loadMemoryServices()
		if err != nil {
			fail(w, 500, "记忆设置暂不可读")
			return
		}
		audit, err := s.manager.memory.ReadReplyAudit(r.Context(), scope, r.PathValue("id"), services.Settings.AuditConfig())
		if err != nil {
			fail(w, 404, "回复依据已过期或不可用")
			return
		}
		respond(w, 200, audit)
	}))
	// A grounding diagnosis is deliberately an operator action.  It can use the
	// configured model, but it is never part of the automatic reply path and has
	// no access to a send operation.
	mux.HandleFunc("POST /api/memory/{scope}/reply-audits/{id}/diagnose", s.protected(s.diagnoseReplyAudit))
}

func (s *Server) diagnoseReplyAudit(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if !decode(w, r, &request) {
		return
	}
	scopeID := r.PathValue("scope")
	scope, err := s.manager.memoryScope(scopeID)
	if err != nil {
		fail(w, http.StatusNotFound, "会话不存在")
		return
	}
	services, err := s.manager.loadMemoryServices()
	if err != nil {
		fail(w, http.StatusInternalServerError, "记忆设置暂不可读")
		return
	}
	audit, err := s.manager.memory.ReadReplyAudit(r.Context(), scopeID, r.PathValue("id"), services.Settings.AuditConfig())
	if err != nil {
		fail(w, http.StatusConflict, "这份回复依据已过期、来源已变化或不可用于诊断")
		return
	}
	decision, ok := replyAuditCandidate(scope, audit)
	if !ok {
		fail(w, http.StatusConflict, "这份记录没有保留可核对的草稿；不会凭空重新生成或发送")
		return
	}
	in, ok := replyAuditInput(scope, audit)
	if !ok {
		fail(w, http.StatusConflict, "触发消息已不在当前可读取上下文中，不能安全诊断")
		return
	}
	cfg := s.manager.runtimeConfig()
	provider, err := s.manager.makeModel(cfg)
	if err != nil {
		fail(w, http.StatusBadRequest, "无法创建诊断模型："+deepseek.UserMessage(err))
		return
	}
	engine, err := companion.New(cfg.sharedCompanion(), s.manager.memory, provider, cfg.SystemPrompt)
	if err != nil {
		fail(w, http.StatusBadRequest, "角色或诊断设置无效")
		return
	}
	// Preview prevents this diagnostic from changing memory/audit retention and
	// guarantees it cannot become a candidate for the delivery queue.
	in.Preview = true
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	s.manager.record(bot.Record{Event: "reply_diagnosis_started", Category: "decision", ThreadID: scope.Thread, MessageID: audit.TriggerMessageID, Content: "手动诊断已保存草稿（不会发送微信）", Status: "running", Origin: "manual"})
	diagnosed, err := engine.DiagnoseGroundedReply(ctx, in, decision)
	if err != nil {
		record := bot.Record{Event: "reply_diagnosis_failed", Category: "error", ThreadID: scope.Thread, MessageID: audit.TriggerMessageID, Content: "手动诊断未完成；原草稿未修改且没有发送微信", Reason: "manual_reply_diagnosis_failed", Status: "failed", Origin: "manual"}
		if diagnostic, exists := deepseek.ErrorDiagnostics(err); exists {
			record.ModelAttempts, record.ModelMaxAttempts = diagnostic.Attempts, diagnostic.MaxAttempts
			record.ModelHTTPStatus, record.ModelStage, record.DurationMS = diagnostic.HTTPStatus, diagnostic.Stage, diagnostic.DurationMS
		}
		s.manager.record(record)
		fail(w, http.StatusBadGateway, "手动诊断未完成："+deepseek.UserMessage(err)+"；没有发送或修改原草稿")
		return
	}
	s.manager.record(bot.Record{Event: "reply_diagnosis_completed", Category: "decision", ThreadID: scope.Thread, MessageID: audit.TriggerMessageID, Content: "手动诊断完成；结果仅供查看，未发送微信", Status: "completed", Origin: "manual"})
	respond(w, http.StatusOK, map[string]any{"diagnosis": diagnosed, "dry_run": true, "wechat_sends": 0, "audit_id": audit.ID})
}

// replyAuditCandidate only accepts a saved audit candidate or the matching
// accepted assistant turn. It intentionally does not call Respond: a missing
// old draft is a diagnostic boundary, not permission to recreate it with the
// current model.
func replyAuditCandidate(scope companion.Scope, audit companion.ReplyAudit) (companion.Decision, bool) {
	if hasReplyText(audit.Messages) {
		return companion.Decision{Action: "reply", Messages: append([]string(nil), audit.Messages...), ReplyContexts: append([]companion.ReplyContext(nil), audit.Contexts...)}, true
	}
	var value any
	if len(audit.Snapshot) > 0 && json.Unmarshal(audit.Snapshot, &value) != nil {
		return companion.Decision{}, false
	}
	var visit func(any) (companion.Decision, bool)
	visit = func(node any) (companion.Decision, bool) {
		switch item := node.(type) {
		case map[string]any:
			for _, key := range []string{"candidate", "repaired_candidate", "decision"} {
				if raw, exists := item[key]; exists {
					encoded, _ := json.Marshal(raw)
					var decision companion.Decision
					if json.Unmarshal(encoded, &decision) == nil && decision.Action == "reply" && hasReplyText(decision.Messages) {
						return decision, true
					}
				}
			}
			for _, child := range item {
				if decision, ok := visit(child); ok {
					return decision, true
				}
			}
		case []any:
			for _, child := range item {
				if decision, ok := visit(child); ok {
					return decision, true
				}
			}
		}
		return companion.Decision{}, false
	}
	if value != nil {
		if decision, ok := visit(value); ok {
			return decision, true
		}
	}
	// Normal successful replies deliberately retain no full model I/O snapshot.
	// The accepted assistant turn is the durable, user-visible candidate and is
	// therefore the only fallback we use for a later manual diagnosis.
	for i := len(scope.Messages) - 1; i >= 0; i-- {
		turn := scope.Messages[i]
		if turn.Role != "assistant" || strings.TrimSpace(turn.Text) == "" || turn.ReplyContext == nil || turn.ReplyContext.AnchorMessageID != audit.TriggerMessageID {
			continue
		}
		return companion.Decision{Action: "reply", Messages: []string{turn.Text}, ReplyContexts: []companion.ReplyContext{*turn.ReplyContext}}, true
	}
	return companion.Decision{}, false
}

func hasReplyText(messages []string) bool {
	for _, text := range messages {
		if strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

// The source must still be represented by the authorized conversation.  We do
// not reconstruct it from an audit snapshot, because snapshots are diagnostic
// artifacts and never a source of chat identity.
func replyAuditInput(scope companion.Scope, audit companion.ReplyAudit) (companion.Input, bool) {
	for _, turn := range scope.Messages {
		if turn.TraceID != audit.TriggerMessageID || turn.Role != "user" {
			continue
		}
		in := companion.Input{Channel: scope.Channel, Account: scope.Account, Thread: scope.Thread, Sender: turn.Sender, SenderName: turn.SenderName, SenderNameSource: turn.SenderNameSource, Text: turn.Text, TraceID: turn.TraceID, Source: turn.Source, QuotedSource: turn.QuotedSource, Addressees: append([]string(nil), turn.Addressees...), IncomingMedia: append([]companion.IncomingMediaRef(nil), turn.IncomingMedia...), ReplyContext: turn.ReplyContext, HistoryObserved: true}
		in.IsGroup = strings.HasSuffix(scope.Thread, "@chatroom")
		if in.IsGroup {
			in.Mode = "social"
		} else {
			in.Mode, in.Directed = "direct", true
		}
		if in.Sender == "" {
			in.Sender = audit.Sender
		}
		return in, in.Sender != "" && strings.TrimSpace(in.Text) != ""
	}
	return companion.Input{}, false
}

func (s *Server) replyAuditList(w http.ResponseWriter, r *http.Request, scope string) {
	if _, err := s.manager.memoryScope(scope); err != nil {
		fail(w, 404, "会话不存在")
		return
	}
	limit, offset := 50, 0
	for key, dst := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || key == "limit" && value == 0 {
				fail(w, 400, "分页参数无效")
				return
			}
			*dst = value
		}
	}
	services, err := s.manager.loadMemoryServices()
	if err != nil {
		fail(w, 500, "记忆设置暂不可读")
		return
	}
	items, more, err := s.manager.memory.FindReplyAudits(r.Context(), scope, r.URL.Query().Get("message_id"), offset, limit, services.Settings.AuditConfig())
	if err != nil {
		fail(w, 500, "回复依据暂不可读")
		return
	}
	respond(w, 200, map[string]any{"scope": scope, "items": items, "has_more": more, "next_offset": offset + len(items), "effective_limit": limit})
}
