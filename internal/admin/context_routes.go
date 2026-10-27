package admin

import (
	"context"
	"errors"
	"net/http"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func (s *Server) contextRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/companion/memory/{id}/compress", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct{}
		if !decode(w, r, &in) {
			return
		}
		scope, err := s.manager.memoryScope(r.PathValue("id"))
		if err != nil {
			fail(w, 404, err.Error())
			return
		}
		cfg := s.manager.runtimeConfig()
		trace := "compression-" + randomToken()
		s.manager.record(bot.Record{Event: "context_compression_started", Category: "tool", ThreadID: scope.Thread, MessageID: trace, Content: "手动压缩上下文", Status: "running", Origin: "manual"})
		reject := func(err error) {
			code, message, status := compressionFailure(err)
			record := bot.Record{Event: "context_compression_failed", Category: "error", ThreadID: scope.Thread, MessageID: trace, Reason: code, Content: message, Status: "failed", Origin: "manual"}
			if diagnostic, ok := deepseek.ErrorDiagnostics(err); ok {
				record.ModelAttempts, record.ModelMaxAttempts, record.ModelHTTPStatus = diagnostic.Attempts, diagnostic.MaxAttempts, diagnostic.HTTPStatus
				record.ModelStage, record.DurationMS = diagnostic.Stage, diagnostic.DurationMS
			}
			s.manager.record(record)
			respond(w, status, map[string]any{"error": message, "code": code})
		}
		provider, err := s.manager.makeModel(cfg)
		if err != nil {
			reject(err)
			return
		}
		engine, err := companion.New(cfg.Companion, s.manager.memory, provider, cfg.SystemPrompt)
		if err != nil {
			reject(err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		changed, err := engine.CompressContext(ctx, companion.Input{Channel: scope.Channel, Account: scope.Account, Thread: scope.Thread}, true)
		if err != nil {
			reject(err)
			return
		}
		s.manager.record(bot.Record{Event: "context_compression_completed", Category: "decision", ThreadID: scope.Thread, MessageID: trace, Content: "会话压缩处理完成", Status: "completed", Origin: "manual"})
		respond(w, 200, map[string]any{"compressed": changed, "scope_id": scope.ID})
	}))
}

func compressionFailure(err error) (string, string, int) {
	if diagnostic, ok := deepseek.ErrorDiagnostics(err); ok {
		return diagnostic.Code, deepseek.UserMessage(err) + "；原始记忆保留，未写入不完整摘要", 502
	}
	switch {
	case errors.Is(err, companion.ErrCompressionInProgress):
		return "context_compression_in_progress", "这个会话正在压缩，其他会话仍可独立压缩", 409
	case errors.Is(err, companion.ErrSummaryInvalid):
		return "context_summary_invalid", "模型摘要格式或长度仍不合要求，已尝试一次格式修复；原始记忆保留", 502
	case errors.Is(err, companion.ErrContextChanged):
		return "context_changed_during_compression", "会话内容在压缩期间持续变化，已重新读取后再试一次；原始记忆保留，可稍后重试", 409
	case errors.Is(err, context.DeadlineExceeded):
		return "context_compression_timeout", "压缩处理超时，未保存不完整摘要；原始记忆保留", 504
	case errors.Is(err, context.Canceled):
		return "context_compression_cancelled", "压缩请求已中止，原始记忆保留", 408
	}
	return "context_compression_storage_or_configuration", "压缩未完成，请检查模型配置与本地存储权限、磁盘空间；原始记忆保留", 502
}
