package companion

import (
	"context"
	"errors"
	"math"
	"time"
	"wechat-bot/internal/deepseek"
)

// Grounding budgets are independent from the conversational answer budget.
// Values are configurable starting points, never silent precision/call caps.
type GroundingSettings struct {
	Enabled         bool   `json:"enabled"`
	Model           string `json:"model,omitempty"`
	MaxTokens       int    `json:"max_tokens"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func GroundingDefaults() GroundingSettings {
	return GroundingSettings{MaxTokens: 8192, TimeoutSeconds: 120, ReasoningEffort: "high"}
}
func (c Config) GroundingConfig() GroundingSettings {
	settings := GroundingDefaults()
	if c.Grounding != nil {
		settings.Enabled = c.Grounding.Enabled
		settings.Model = c.Grounding.Model
		if c.Grounding.MaxTokens != 0 {
			settings.MaxTokens = c.Grounding.MaxTokens
		}
		if c.Grounding.TimeoutSeconds != 0 {
			settings.TimeoutSeconds = c.Grounding.TimeoutSeconds
		}
		if c.Grounding.ReasoningEffort != "" {
			settings.ReasoningEffort = c.Grounding.ReasoningEffort
		}
	}
	return settings
}

func (c Config) GroundingOptions() (deepseek.GroundingOptions, error) {
	s := c.GroundingConfig()
	if s.TimeoutSeconds < 0 || int64(s.TimeoutSeconds) > math.MaxInt64/int64(time.Second) {
		return deepseek.GroundingOptions{}, errors.New("grounding review timeout is invalid")
	}
	return (deepseek.GroundingOptions{Model: s.Model, MaxTokens: s.MaxTokens, Timeout: time.Duration(s.TimeoutSeconds) * time.Second, ReasoningEffort: s.ReasoningEffort}).Normalized()
}

type groundingLanguageModel interface {
	ChatGrounding(context.Context, string, []map[string]string, deepseek.GroundingOptions) (string, error)
}

type validatedGroundingLanguageModel interface {
	ChatGroundingValidated(context.Context, string, []map[string]string, deepseek.GroundingOptions, func(string) error) (string, error)
}

func (e *Engine) groundingChat(ctx context.Context, system string, messages []map[string]string) (string, error) {
	return e.groundingChatValidated(ctx, system, messages, nil)
}

func (e *Engine) groundingChatValidated(ctx context.Context, system string, messages []map[string]string, validate func(string) error) (string, error) {
	evidence, err := e.captureGenerationEvidence(ctx)
	if err != nil {
		return "", err
	}
	ctx = withGenerationEvidence(ctx, evidence)
	options, err := e.Config.GroundingOptions()
	if err != nil {
		return "", err
	}
	settings := e.memorySettings()
	if err := ValidateContextWindow(system, messages, nil, settings.ContextWindow, options.MaxTokens); err != nil {
		return "", err
	}
	if err := e.validateGenerationSources(ctx); err != nil {
		return "", err
	}
	var answer string
	if model, ok := e.model.(validatedGroundingLanguageModel); ok && validate != nil {
		answer, err = model.ChatGroundingValidated(ctx, system, messages, options, validate)
	} else if model, ok := e.model.(groundingLanguageModel); ok {
		answer, err = model.ChatGrounding(ctx, system, messages, options)
	} else {
		// Synthetic/unit adapters implement Chat only. This path is explicit in the
		// request metadata and log; actual clients implement the dedicated method.
		if e.ToolLog != nil {
			e.ToolLog("grounding_unconfigured_chat_adapter")
		}
		answer, err = e.model.Chat(ctx, system, messages)
	}
	if err != nil {
		return "", err
	}
	if err := e.ValidateGenerationEvidence(ctx, evidence); err != nil {
		return "", err
	}
	return answer, nil
}

func (e *Engine) groundingTransport() string {
	if _, ok := e.model.(groundingLanguageModel); ok {
		return "dedicated_structured_review"
	}
	return "unconfigured_chat_adapter"
}
