package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// GroundingOptions applies only to the read-only structured review request.
// MaxTokens includes provider reasoning tokens as well as the final JSON.
// Zero values select defaults; explicit values are never silently clamped.
type GroundingOptions struct {
	Model           string        `json:"model,omitempty"`
	MaxTokens       int           `json:"max_tokens"`
	Timeout         time.Duration `json:"timeout"`
	ReasoningEffort string        `json:"reasoning_effort"`
}

// Normalized returns the actual options so callers can persist the review
// configuration alongside the model and quality report. The output bound is
// DeepSeek's documented Chat Completions API bound, not the normal reply budget.
func (o GroundingOptions) Normalized() (GroundingOptions, error) {
	if len(o.Model) > 128 || !utf8.ValidString(o.Model) || strings.IndexFunc(o.Model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return GroundingOptions{}, errors.New("invalid grounding model")
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = 8192
	}
	if o.Timeout == 0 {
		o.Timeout = 120 * time.Second
	}
	if o.ReasoningEffort == "" {
		o.ReasoningEffort = "high"
	}
	if o.MaxTokens < 1 || o.MaxTokens > 393216 || o.Timeout < 0 {
		return GroundingOptions{}, errors.New("invalid grounding request budget")
	}
	if o.ReasoningEffort != "low" && o.ReasoningEffort != "high" && o.ReasoningEffort != "max" {
		return GroundingOptions{}, errors.New("invalid grounding reasoning effort")
	}
	return o, nil
}

// ChatGrounding uses the configured or explicitly selected review model with a
// separate JSON/reasoning budget. The endpoint and credential remain unchanged.
// It exposes only final content and provider-reported usage, never reasoning
// text. Compatible providers receive standard JSON mode without DeepSeek-only
// fields; unsupported JSON mode is a failure, not a plain-text fallback.
// A completed, nonempty final response with invalid JSON syntax may receive one
// format repair under the original deadline, model and output budget. Valid
// JSON verdicts are never resampled here, even if they reject the candidate.
func (c *Client) ChatGrounding(ctx context.Context, system string, messages []map[string]string, options GroundingOptions) (string, error) {
	return c.ChatGroundingValidated(ctx, system, messages, options, nil)
}

// ChatGroundingValidated shares the existing two format attempts between JSON
// syntax and host protocol validation. validate must reject malformed protocol
// only: an authentic reject/uncertain verdict is a valid result, never a reason
// to sample another verdict. Neither previous output nor hidden reasoning is
// put into the repair request; the original candidate and evidence stay fixed.
func (c *Client) ChatGroundingValidated(ctx context.Context, system string, messages []map[string]string, options GroundingOptions, validate func(string) error) (string, error) {
	if c.thinkingConfigured {
		options.Model = c.model
		options.MaxTokens = c.maxTokens
		options.ReasoningEffort = c.reasoningEffort
	}
	options, err := options.Normalized()
	if err != nil {
		return "", err
	}
	if len(messages) == 0 || len(messages) > 101 {
		return "", errors.New("invalid conversation")
	}
	// Freeze the caller's evidence values before either request. A usage or
	// request observer must not accidentally change the second candidate.
	frozen := make([]map[string]string, len(messages))
	for i, message := range messages {
		frozen[i] = map[string]string{"role": message["role"], "content": message["content"]}
	}
	if ctx.Value(purposeKey{}) == nil {
		ctx = WithPurpose(ctx, "reply_grounding_check")
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	copy := *c
	if options.Model != "" {
		copy.model = options.Model
	}
	copy.maxTokens = options.MaxTokens
	copy.rejectToolCalls = true
	// Reasoning is part of the HTTP envelope even though it is not decoded into
	// the returned message. Keep an explicit local resource bound independent
	// from the short conversational response limit.
	copy.responseLimitBytes = 16 << 20
	httpCopy := *c.http
	httpCopy.Timeout = options.Timeout
	copy.http = &httpCopy
	start := time.Now()
	purpose, _ := ctx.Value(purposeKey{}).(string)
	originalObserver, _ := ctx.Value(requestObserverKey{}).(func(string, Diagnostics))
	var last Diagnostics
	ctx = WithRequestObserver(ctx, func(event string, d Diagnostics) {
		if event == "model_grounding_response" {
			last = d
		}
		if originalObserver != nil {
			originalObserver(event, d)
		}
	})
	var protocolCode string
	for attempt := 1; attempt <= 2; attempt++ {
		attemptCtx := context.WithValue(ctx, groundingFormatAttemptKey{}, attempt)
		policy := system
		if attempt == 2 {
			attemptCtx = WithPurpose(attemptCtx, purpose+"_format_repair")
			policy += "\n【JSON格式修复】上次最终输出未通过JSON语法或宿主协议校验。输入候选和证据完全相同；重新核对原协议要求的units、全部句段编号、状态与来源编号，只输出符合原协议的一个合法JSON对象，不输出Markdown围栏或JSON外文字。不得改写候选、执行工具、把无证据内容改标为已支持或降低原核验标准。"
			if protocolCode != "" {
				policy += "\n宿主校验项：" + protocolCode
			}
		}
		answer, callErr := copy.chatWithGrounding(attemptCtx, policy, frozen, true, &options)
		if callErr != nil {
			return "", callErr
		}
		var validationErr error
		if json.Valid([]byte(answer)) {
			if validate == nil {
				if attempt > 1 {
					observeRequest(ctx, "model_grounding_format_recovered", last)
				}
				return answer, nil
			}
			validationErr = validate(answer)
			if validationErr == nil {
				if attempt > 1 {
					observeRequest(ctx, "model_grounding_format_recovered", last)
				}
				return answer, nil
			}
		}
		last.Code, last.Stage, last.Retryable = "model_invalid_response", "decode", false
		protocolCode = ""
		if validationErr != nil {
			last.Code, last.Stage = "model_review_protocol_invalid", "review_protocol"
			var coded interface{ ReviewProtocolCode() string }
			if !errors.As(validationErr, &coded) {
				// Changed authorization/evidence, cancellation and other host
				// failures cannot be repaired by sampling another review.
				return "", validationErr
			} else {
				code := coded.ReviewProtocolCode()
				if len(code) <= 80 && strings.HasPrefix(code, "review_") && strings.IndexFunc(code, func(r rune) bool { return r != '_' && (r < 'a' || r > 'z') }) < 0 {
					protocolCode = code
				}
			}
		}
		last.ProtocolCode = protocolCode
		last.DurationMS = time.Since(start).Milliseconds()
		observeRequest(ctx, "model_grounding_validation_failed", last)
		if attempt == 2 {
			return "", &RequestError{Diagnostics: last, cause: validationErr}
		}
		observeRequest(ctx, "model_grounding_format_repair", last)
	}
	panic("unreachable bounded grounding format repair")
}
