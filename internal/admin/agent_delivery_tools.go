package admin

import (
	"context"
	"encoding/json"
	"errors"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func (p *permittedTools) callDelivery(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var in struct {
		Job    string `json:"job_id"`
		Key    string `json:"request_key"`
		Thread string `json:"thread_id"`
	}
	if json.Unmarshal(args, &in) != nil || !validID(in.Job) {
		return "", toolInputError("需要原发送任务编号")
	}
	if in.Thread == "" {
		in.Thread = p.trace.Thread
	}
	if !p.conversationReadAllowed(in.Thread) {
		return "", toolInputError("未授权跨会话核对发送任务")
	}
	native, ok := p.manager.native.(nativeTools)
	if !ok {
		return "", toolInputError("原生发送回执不可用")
	}
	status, err := native.ToolSendStatus(ctx, in.Job)
	if err != nil {
		return "", toolInputError("暂未取得原发送任务状态；未重复提交")
	}
	if status.ID != in.Job || status.ThreadID != in.Thread {
		return "", toolInputError("发送任务不属于当前会话")
	}
	// A verified delivery record is cross-conversation evidence. It may route a
	// later natural-language result to the requesting owner, but never changes
	// the actual delivery target or grants send authority.
	p.routeCrossConversationThread(in.Thread)
	if name == "wechat_delivery_status" {
		b, _ := json.Marshal(status)
		var result map[string]any
		_ = json.Unmarshal(b, &result)
		result["retryable"] = status.Retryable
		if reader, ok := p.manager.native.(nativeCatalog); ok && (status.State == "needs_review" || status.State == "unknown") {
			proof, scanErr := linuxnative.ReconcileMedia(ctx, reader, status)
			result["chat_reconciliation"] = proof
			if scanErr != nil {
				result["next_action"] = "聊天读取失败，保存检查点稍后核对；不能据此断言未发送"
			} else {
				result["next_action"] = "已核对同群真实聊天；用wechat_delivery_retry续办原任务，工具会保留已接受和同步确认的分段。retryable=false只是底层原来的自动续传标记，不是停止恢复的理由"
			}
		}
		b, err := json.Marshal(result)
		return string(b), err
	}
	if !p.privileged() || in.Key == "" || len(in.Key) > 128 {
		return "", toolInputError("仅可信主人可续传，需提供稳定请求编号")
	}
	if status.CancelRequested {
		return "", toolInputError("用户已取消原发送，停止补发")
	}
	allowed := false
	for _, kind := range []string{"voice", "file", "image", "video"} {
		if _, exists := p.manager.tools.source(status.SourceID, p.trace.Account, kind); exists && companion.MediaPermitted(p.manager.Config().Companion.Permissions, kind) {
			allowed = true
		}
	}
	if !allowed {
		return "", toolInputError("原媒体来源未获当前发送权限；未续传")
	}
	retry, ok := p.manager.native.(interface {
		RetryMediaJob(context.Context, string) (linuxnative.ToolStatus, error)
	})
	if !ok {
		return "", toolInputError("原生后台不支持原任务续传")
	}
	return p.privilegedCall(ctx, name, args, func() (any, error) {
		if !p.privileged() || !p.conversationReadAllowed(in.Thread) {
			return nil, errors.New("发送授权已撤销")
		}
		// The normal native flag no longer permanently blocks reconciliation.
		// Real implementation reads this thread again immediately before retry.
		if !status.Retryable {
			if reconciler, ok := p.manager.native.(interface {
				ReconcileMediaJob(context.Context, string) (linuxnative.ToolStatus, error)
			}); ok {
				if err := p.deliveryDelay(ctx); err != nil {
					return nil, err
				}
				if err := p.manager.checkWechatSubmission(ctx); err != nil {
					return nil, err
				}
				return reconciler.ReconcileMediaJob(ctx, in.Job)
			}
			return nil, errors.New("当前原生版本缺少聊天核对后恢复接口，需要修复服务；保存任务继续处理")
		}
		if err := p.deliveryDelay(ctx); err != nil {
			return nil, err
		}
		if err := p.manager.checkWechatSubmission(ctx); err != nil {
			return nil, err
		}
		return retry.RetryMediaJob(ctx, in.Job)
	})
}
