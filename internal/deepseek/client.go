// Package deepseek implements bounded, non-streaming text completion calls.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const LegacySystemPrompt = "你是微信群中的 AI 助手。用简洁、清楚的中文回答当前问题，尽量控制在500字以内。你只能生成回复文字，不能声称执行了外部操作。"
const DefaultSystemPrompt = "你是微信群中的 AI 伙伴。用简短自然的中文回应当前话题，优先遵循配置的角色与会话权限。只有程序明确提供的能力才可使用；对联网、生成媒体和外部操作，必须以真实返回结果为依据，不能编造已经完成。"

type Config struct {
	BaseURL, KeyFile, Model string
	SystemPrompt            string
	MaxTokens               int
	ToolMaxTokens           int
	Compatible              bool
	OnUsage                 func(Usage)
}
type Client struct {
	endpoint, key, model string
	systemPrompt         string
	maxTokens            int
	toolMaxTokens        int
	compatible           bool
	http                 *http.Client
	onUsage              func(Usage)
}

func New(config Config) (*Client, error) {
	if config.BaseURL == "" {
		config.BaseURL = "https://api.deepseek.com"
	}
	if config.Model == "" {
		config.Model = "deepseek-flash"
	}
	if config.SystemPrompt == "" {
		config.SystemPrompt = DefaultSystemPrompt
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 768
	}
	if config.ToolMaxTokens == 0 {
		config.ToolMaxTokens = config.MaxTokens
	}
	if config.ToolMaxTokens < 64 || config.ToolMaxTokens > 4096 {
		return nil, errors.New("invalid tool output limit")
	}
	if len(config.SystemPrompt) > 16384 || !utf8.ValidString(config.SystemPrompt) || config.MaxTokens < 64 || config.MaxTokens > 4096 {
		return nil, errors.New("invalid model role or output limit")
	}
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("DeepSeek base URL must be HTTPS")
	}
	if len(config.Model) > 128 || strings.ContainsAny(config.Model, "\r\n\x00") {
		return nil, errors.New("invalid model")
	}
	f, err := os.Open(config.KeyFile)
	if err != nil {
		return nil, errors.New("DeepSeek key file unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	key := strings.TrimSpace(string(raw))
	if err != nil || len(raw) > 4096 || len(key) < 16 || !utf8.ValidString(key) || strings.ContainsAny(key, "\r\n\x00 ") {
		return nil, errors.New("invalid DeepSeek key file")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return &Client{endpoint: u.String(), key: key, model: config.Model, systemPrompt: config.SystemPrompt, maxTokens: config.MaxTokens, toolMaxTokens: config.ToolMaxTokens, compatible: config.Compatible, onUsage: config.OnUsage, http: &http.Client{
		Timeout:       90 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 2},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") },
	}}, nil
}

func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" || len(prompt) > 4096 || !utf8.ValidString(prompt) {
		return "", errors.New("invalid model input")
	}
	return c.Chat(ctx, c.systemPrompt, []map[string]string{{"role": "user", "content": prompt}})
}

// Chat accepts only text user/assistant turns. The caller supplies a separate
// trusted system policy; historical group messages cannot become system roles.
func (c *Client) Chat(ctx context.Context, system string, messages []map[string]string) (string, error) {
	if strings.TrimSpace(system) == "" || len(system) > 96<<10 || !utf8.ValidString(system) || len(messages) == 0 || len(messages) > 101 {
		return "", errors.New("invalid conversation")
	}
	all := []map[string]string{{"role": "system", "content": system}}
	total := len(system)
	for _, message := range messages {
		role, text := message["role"], message["content"]
		if (role != "user" && role != "assistant") || text == "" || len(text) > 96<<10 || !utf8.ValidString(text) {
			return "", errors.New("invalid conversation turn")
		}
		total += len(text)
		all = append(all, map[string]string{"role": role, "content": text})
	}
	if total > 192<<10 {
		return "", errors.New("conversation exceeds input bound")
	}
	input := struct {
		Model          string              `json:"model"`
		Messages       []map[string]string `json:"messages"`
		Stream         bool                `json:"stream"`
		MaxTokens      int                 `json:"max_tokens"`
		Thinking       map[string]string   `json:"thinking,omitempty"`
		ResponseFormat map[string]string   `json:"response_format,omitempty"`
	}{c.model, all, false, c.maxTokens, nil, nil}
	if strings.Contains(system, "【可自主使用的工具】") {
		input.ResponseFormat = map[string]string{"type": "json_object"}
	}
	if !c.compatible {
		input.Thinking = map[string]string{"type": "disabled"}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return "", errors.New("model input encoding failed")
	}
	return c.request(ctx, body)
}
func (c *Client) requestOnce(ctx context.Context, body []byte) (string, *RequestError) {
	usage := Usage{Time: time.Now().UTC()}
	defer func() {
		if c.onUsage != nil {
			c.onUsage(usage)
		}
	}()
	var err error
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", requestFailure("model_invalid_request", "request", 0, false)
	}
	request.GetBody = nil // Transport replay is disabled; recovery.go owns the bounded retry.
	request.Header.Set("Authorization", "Bearer "+c.key)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if response != nil && response.StatusCode >= 300 && response.StatusCode < 400 {
			return "", requestFailure("model_redirect_denied", "http_status", response.StatusCode, false)
		}
		return "", transportFailure(ctx, err, "connection_or_headers")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		failure := statusFailure(response.StatusCode)
		failure.retryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now())
		return "", failure
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil {
		failure := transportFailure(ctx, err, "response_body")
		failure.HTTPStatus = response.StatusCode
		return "", failure
	}
	if len(data) > 256<<10 {
		return "", requestFailure("model_response_too_large", "response_body", 200, false)
	}
	usage = parseUsage(data, usage.Time)
	var result struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Choices) != 1 {
		return "", requestFailure("model_invalid_response", "decode", 200, false)
	}
	choice := result.Choices[0]
	if choice.FinishReason == "tool_calls" {
		if len(choice.Message.ToolCalls) == 0 || len(choice.Message.ToolCalls) > 8 {
			return "", requestFailure("model_tool_call_count_invalid", "decode", 200, false)
		}
		calls := []json.RawMessage{}
		for index, item := range choice.Message.ToolCalls {
			call := item.Function
			if len(call.Arguments) > 16<<10 {
				return "", requestFailure("model_tool_arguments_too_large", "decode", 200, false)
			}
			args := bytes.TrimSpace([]byte(call.Arguments))
			if !functionNameAllowed(call.Name) || len(call.Name) >= 130 || len(args) == 0 || args[0] != '{' || !json.Valid(args) {
				return "", requestFailure("model_tool_arguments_invalid", "decode", 200, false)
			}
			value := map[string]any{"action": "tool", "tool": call.Name, "arguments": json.RawMessage(args)}
			if index == 0 && strings.TrimSpace(choice.Message.Content) != "" && len(choice.Message.Content) <= 300 {
				value["progress"] = strings.TrimSpace(choice.Message.Content)
			}
			out, _ := json.Marshal(value)
			calls = append(calls, out)
		}
		if len(calls) == 1 {
			return string(calls[0]), nil
		}
		out, _ := json.Marshal(map[string]any{"action": "tool_batch", "calls": calls})
		if len(out) > 32<<10 {
			return "", requestFailure("model_tool_arguments_too_large", "decode", 200, false)
		}
		return string(out), nil
	}
	if choice.FinishReason == "length" {
		return "", requestFailure("model_output_token_limit", "decode", 200, false)
	}
	if choice.FinishReason == "content_filter" {
		return "", requestFailure("model_content_filtered", "decode", 200, false)
	}
	if choice.FinishReason != "stop" {
		return "", requestFailure("model_response_incomplete", "decode", 200, false)
	}
	answer := strings.TrimSpace(result.Choices[0].Message.Content)
	if answer == "" || !utf8.ValidString(answer) {
		return "", requestFailure("model_response_empty", "decode", 200, answer == "")
	}
	return answer, nil
}
