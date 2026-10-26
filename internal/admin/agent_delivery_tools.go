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
		Job string `json:"job_id"`
		Key string `json:"request_key"`
	}
	if json.Unmarshal(args, &in) != nil || !validID(in.Job) {
		return "", errors.New("需要原发送任务编号")
	}
	native, ok := p.manager.native.(nativeTools)
	if !ok {
		return "", errors.New("原生发送回执不可用")
	}
	status, err := native.ToolSendStatus(ctx, in.Job)
	if err != nil {
		return "", errors.New("暂未取得原发送任务状态；未重复提交")
	}
	if status.ID != in.Job || status.ThreadID != p.trace.Thread {
		return "", errors.New("发送任务不属于当前会话")
	}
	if name == "wechat_delivery_status" {
		b, _ := json.Marshal(status)
		var result map[string]any
		_ = json.Unmarshal(b, &result)
		result["retryable"] = status.Retryable
		b, err := json.Marshal(result)
		return string(b), err
	}
	if !p.privileged() || in.Key == "" || len(in.Key) > 128 {
		return "", errors.New("仅可信主人可续传，需提供稳定请求编号")
	}
	if !status.Retryable || status.CancelRequested {
		return "", errors.New("原生任务暂不允许续传：回执不确定或已取消。保持核对原任务、已接受分段不重发；不得换编号重发整份媒体")
	}
	allowed := false
	for _, kind := range []string{"voice", "file", "image", "video"} {
		if _, exists := p.manager.tools.source(status.SourceID, p.trace.Account, kind); exists && companion.MediaPermitted(p.manager.Config().Companion.Permissions, kind) {
			allowed = true
		}
	}
	if !allowed {
		return "", errors.New("原媒体来源未获当前发送权限；未续传")
	}
	retry, ok := p.manager.native.(interface {
		RetryMediaJob(context.Context, string) (linuxnative.ToolStatus, error)
	})
	if !ok {
		return "", errors.New("原生后台不支持原任务续传")
	}
	return p.privilegedCall(ctx, name, args, func() (any, error) { return retry.RetryMediaJob(ctx, in.Job) })
}
