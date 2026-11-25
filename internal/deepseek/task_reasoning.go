package deepseek

import (
	"context"
	"errors"
)

// TaskReasoning is request-scoped. It never mutates the shared client or the
// saved daily-chat settings, including when independent runs execute together.
type TaskReasoning struct {
	Mode      string `json:"mode"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type taskReasoningKey struct{}

func (p TaskReasoning) Validate() error {
	switch p.Mode {
	case "inherit", "off", "low", "high", "max":
	default:
		return errors.New("task reasoning mode must be inherit, off, low, high or max")
	}
	if p.MaxTokens != 0 && (p.MaxTokens < 64 || p.MaxTokens > 393216) {
		return errors.New("task output budget must be between 64 and 393216 tokens")
	}
	return nil
}

func WithTaskReasoning(ctx context.Context, p TaskReasoning) (context.Context, error) {
	if err := p.Validate(); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, taskReasoningKey{}, p), nil
}

func (c *Client) SupportsTaskReasoning() bool { return !c.compatible }

func (c *Client) TaskReasoningSettings(ctx context.Context) (TaskReasoning, error) {
	copy, err := c.taskReasoningClient(ctx)
	if err != nil {
		return TaskReasoning{}, err
	}
	mode := "off"
	if copy.thinkingEnabled {
		mode = copy.reasoningEffort
	}
	return TaskReasoning{Mode: mode, MaxTokens: copy.toolMaxTokens}, nil
}

func (c *Client) taskReasoningClient(ctx context.Context) (*Client, error) {
	p, ok := ctx.Value(taskReasoningKey{}).(TaskReasoning)
	if !ok || p.Mode == "inherit" {
		return c, nil
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !c.SupportsTaskReasoning() {
		return nil, errors.New("provider does not declare temporary reasoning support")
	}
	copy := *c
	copy.thinkingConfigured = true
	copy.thinkingEnabled = p.Mode != "off"
	if copy.thinkingEnabled {
		copy.reasoningEffort = p.Mode
	}
	if p.MaxTokens != 0 {
		copy.maxTokens, copy.toolMaxTokens = p.MaxTokens, p.MaxTokens
	} else if copy.thinkingEnabled {
		copy.maxTokens = max(copy.maxTokens, 8192)
		copy.toolMaxTokens = max(copy.toolMaxTokens, 8192)
	}
	return &copy, nil
}
