package admin

import (
	"context"
	"errors"
	"net/http"
	"os"
	"syscall"
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
		if !cfg.Companion.Permissions.Remember {
			fail(w, 409, "此账号已关闭记忆，开启后可压缩会话")
			return
		}
		job, err := s.manager.enqueueContextCompression(scope)
		if err != nil {
			if errors.Is(err, companion.ErrCompressionInProgress) {
				respond(w, 409, map[string]any{"error": "这个会话正在压缩，其他会话仍可独立压缩", "code": "context_compression_in_progress", "job": job})
				return
			}
			respond(w, 409, map[string]any{"error": err.Error(), "code": "context_compression_not_queued"})
			return
		}
		respond(w, http.StatusAccepted, map[string]any{"queued": true, "scope_id": scope.ID, "trace": job.Trace, "status_url": "/api/companion/memory/" + scope.ID + "/compression"})
	}))
	mux.HandleFunc("GET /api/companion/memory/{id}/compression", s.protected(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.manager.memoryScope(r.PathValue("id")); err != nil {
			fail(w, 404, err.Error())
			return
		}
		respond(w, 200, s.manager.compressionStatus(r.PathValue("id")))
	}))
}

func compressionFailure(err error) (string, string, int) {
	if diagnostic, ok := deepseek.ErrorDiagnostics(err); ok {
		return diagnostic.Code, deepseek.UserMessage(err) + "；原始记忆保留，未写入不完整摘要", 502
	}
	switch {
	case errors.Is(err, companion.ErrCompressionInProgress):
		return "context_compression_in_progress", "这个会话正在压缩，其他会话仍可独立压缩", 409
	case errors.Is(err, companion.ErrCompressionPermissionRevoked):
		return "context_compression_permission_revoked", "记忆权限在压缩完成前已关闭；原始记忆保留，未写入摘要", 409
	case errors.Is(err, companion.ErrSummaryInvalid):
		return "context_summary_invalid", "模型摘要格式或长度仍不合要求，已尝试一次格式修复；原始记忆保留", 502
	case errors.Is(err, companion.ErrSummarySourceMissing):
		return "context_summary_source_missing", "模型摘要缺少事实来源编号，修复后仍不完整；未覆盖原摘要和聊天", 502
	case errors.Is(err, companion.ErrSummarySourceInvalid):
		return "context_summary_source_invalid", "模型摘要引用了本批消息或有效旧摘要之外的来源，修复后仍未通过；未覆盖原摘要和聊天", 502
	case errors.Is(err, os.ErrPermission):
		return "context_compression_permission_denied", "保存摘要时本地文件权限不足；原始记忆保留", 500
	case errors.Is(err, syscall.ENOSPC):
		return "context_compression_disk_full", "保存摘要时磁盘空间不足；原始记忆保留", 500
	case errors.Is(err, companion.ErrContextChanged):
		return "context_changed_during_compression", "会话内容在压缩期间持续变化，已重新读取后再试一次；原始记忆保留，可稍后重试", 409
	case errors.Is(err, context.DeadlineExceeded):
		return "context_compression_timeout", "压缩处理超时，未保存不完整摘要；原始记忆保留", 504
	case errors.Is(err, context.Canceled):
		return "context_compression_cancelled", "压缩请求已中止，原始记忆保留", 408
	}
	return "context_compression_unclassified", "压缩未完成：发生尚未分类的内部错误，不能据此判断为权限或磁盘问题；原始记忆保留", 502
}
