package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
	"wechat-bot/internal/mcptools"
)

// ChatFunctions uses the provider's native tools/tool_calls protocol. The
// application transcript remains replay-safe; only actual executed calls have
// matching tool results. Failed/unexecuted decisions stay ordinary text.
func (c *Client) ChatFunctions(ctx context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	messages := []map[string]any{{"role": "system", "content": system}}
	total := len(system)
	for i := 0; i < len(turns); i++ {
		t := turns[i]
		text := t["content"]
		if len(text) > 96<<10 || !utf8.ValidString(text) {
			return "", errors.New("invalid tool transcript")
		}
		total += len(text)
		if t["role"] == "assistant" && i+1 < len(turns) {
			var call struct {
				Action, Tool string
				Arguments    json.RawMessage
			}
			var result struct{ Tool, Result, Source string }
			if json.Unmarshal([]byte(text), &call) == nil && call.Action == "tool" && json.Unmarshal([]byte(turns[i+1]["content"]), &result) == nil && result.Source == "untrusted_tool_result" && result.Tool == call.Tool {
				id := fmt.Sprintf("call_%d", i)
				messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]string{"name": call.Tool, "arguments": string(call.Arguments)}}}}, map[string]any{"role": "tool", "tool_call_id": id, "content": result.Result})
				total += len(result.Result)
				i++
				continue
			}
		}
		if t["role"] != "user" && t["role"] != "assistant" {
			return "", errors.New("invalid transcript role")
		}
		messages = append(messages, map[string]any{"role": t["role"], "content": text})
	}
	if total > 256<<10 || len(messages) > 120 {
		return "", errors.New("tool transcript exceeds input bound")
	}
	catalog := []any{}
	for _, t := range tools {
		catalog = append(catalog, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
	}
	body := map[string]any{"model": c.model, "messages": messages, "stream": false, "max_tokens": c.maxTokens}
	if len(catalog) > 0 {
		body["tools"] = catalog
		body["tool_choice"] = "auto"
		body["parallel_tool_calls"] = false
	}
	if !c.compatible {
		body["thinking"] = map[string]string{"type": "disabled"}
	}
	data, _ := json.Marshal(body)
	return c.request(ctx, data)
}

func functionNameAllowed(s string) bool { return s != "" && !strings.ContainsAny(s, " \r\n") }
