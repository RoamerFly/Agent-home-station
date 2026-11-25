package companion

import (
	"context"
	"encoding/json"
	"errors"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

func ValidateTaskReasoningMode(mode string) error {
	if mode == "" || mode == "auto" {
		return nil
	}
	return (deepseek.TaskReasoning{Mode: mode}).Validate()
}

var taskReasoningTool = mcptools.Tool{
	Name:        "task_reasoning",
	Description: "临时调整当前执行回合的模型思考模式。由你按任务复杂度决定是否开启 high/max，完成复杂步骤后可用 inherit 恢复日常设置。只影响本轮后续模型请求，结束后自动恢复，不修改配置或其他会话。mode=inherit/off/low/high/max；max_tokens可选，开启思考时默认至少8192，可按需要显式调整。人工指定模式时本工具不提供。",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string","enum":["inherit","off","low","high","max"]},"max_tokens":{"type":"integer","minimum":64,"maximum":393216},"reason":{"type":"string"}},"required":["mode"],"additionalProperties":false}`),
}

func (e *AgentRunner) taskReasoningContext(ctx context.Context) (context.Context, bool, error) {
	mode := e.Config.TaskReasoning
	supported, ok := e.model.(interface{ SupportsTaskReasoning() bool })
	if !ok || !supported.SupportsTaskReasoning() {
		if mode != "" && mode != "auto" && mode != "inherit" {
			return ctx, false, errors.New("当前模型接口未声明临时思考模式能力，请沿用日常设置或使用支持的模型接口")
		}
		return ctx, false, nil
	}
	if mode == "" || mode == "auto" {
		return ctx, true, nil
	}
	ctx, err := deepseek.WithTaskReasoning(ctx, deepseek.TaskReasoning{Mode: mode})
	return ctx, false, err
}
