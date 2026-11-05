package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSharedThinkingAndTransientToolReplay(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, effort := range []string{"low", "high", "max"} {
			t.Run(effort+map[bool]string{false: "-off", true: "-on"}[enabled], func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					calls++
					want := "disabled"
					if enabled {
						want = "enabled"
					}
					if body["model"] != "deepseek-flash" || body["thinking"].(map[string]any)["type"] != want {
						t.Errorf("shared settings lost: %v", body["thinking"])
					}
					if enabled && body["reasoning_effort"] != effort {
						t.Error("wrong strength")
					}
					if calls == 1 {
						w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"reasoning_content":"PRIVATE_TRANSIENT_ONLY","tool_calls":[{"function":{"name":"probe","arguments":"{}"}}]}}]}`))
						return
					}
					if calls == 2 && enabled {
						messages := body["messages"].([]any)
						assistant := messages[1].(map[string]any)
						if assistant["reasoning_content"] != "PRIVATE_TRANSIENT_ONLY" {
							t.Error("provider reasoning was not replayed for the actual tool call")
						}
					}
					w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
				}))
				defer server.Close()
				c := &Client{endpoint: server.URL, model: "deepseek-flash", http: server.Client(), maxTokens: 4096, toolMaxTokens: 4096, thinkingConfigured: true, thinkingEnabled: enabled, reasoningEffort: effort}
				ctx, clearSession := WithThinkingSession(context.Background())
				answer, err := c.ChatFunctions(ctx, "synthetic", []map[string]string{{"role": "user", "content": "probe"}}, nil)
				if err != nil || strings.Contains(answer, "PRIVATE") {
					t.Fatal("reasoning escaped final response", err)
				}
				turn := AssistantTurn(ctx, answer)
				raw, _ := json.Marshal(turn)
				if strings.Contains(string(raw), "PRIVATE") {
					t.Fatal("reasoning entered app transcript")
				}
				_, err = c.ChatFunctions(ctx, "synthetic", []map[string]string{turn, {"role": "user", "content": `{"tool":"probe","source":"untrusted_tool_result","result":"ok"}`}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				// A legacy review override must not change the shared production policy.
				_, err = c.ChatGrounding(ctx, "synthetic", []map[string]string{{"role": "user", "content": "check"}}, GroundingOptions{Model: "deepseek-v4-pro", ReasoningEffort: "max"})
				if err != nil {
					t.Fatal(err)
				}
				clearSession()
				if replayThinking(ctx, turn) != "" {
					t.Fatal("reasoning survived task completion")
				}
			})
		}
	}
}

func TestThinkingReferencesSeparateIdenticalCallsAndSessions(t *testing.T) {
	ctx, clearSession := WithThinkingSession(context.Background())
	defer clearSession()
	rememberThinking(ctx, "identical", "first")
	first := AssistantTurn(ctx, "identical")
	rememberThinking(ctx, "identical", "second")
	second := AssistantTurn(ctx, "identical")
	if replayThinking(ctx, first) != "first" || replayThinking(ctx, second) != "second" {
		t.Fatal("identical actions merged reasoning")
	}
	other, cleanup := WithThinkingSession(context.Background())
	defer cleanup()
	rememberThinking(other, "identical", "foreign")
	if replayThinking(other, first) != "" {
		t.Fatal("reasoning crossed sessions")
	}
	first["content"] = "modified"
	if replayThinking(ctx, first) != "" {
		t.Fatal("reasoning reused for changed action")
	}
}
