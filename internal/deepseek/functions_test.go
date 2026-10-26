package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
)

func TestToolOutputBudgetIsSeparateFromConfiguredChatBudget(t *testing.T) {
	seen := []int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			MaxTokens int `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&v)
		seen = append(seen, v.MaxTokens)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "key")
	os.WriteFile(file, []byte("synthetic-private-test-key"), 0600)
	c, e := New(Config{BaseURL: server.URL, KeyFile: file, MaxTokens: 256, ToolMaxTokens: 4096})
	if e != nil {
		t.Fatal(e)
	}
	c.http = server.Client()
	turns := []map[string]string{{"role": "user", "content": "test"}}
	if _, e = c.Chat(context.Background(), "policy", turns); e != nil {
		t.Fatal(e)
	}
	if _, e = c.ChatFunctions(context.Background(), "policy", turns, nil); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 2 || seen[0] != 256 || seen[1] != 4096 {
		t.Fatal("tool arguments constrained by chat budget or chat budget changed", seen)
	}
}

func TestNativeFunctionsUseToolCallsAndMatchingResults(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Tools    []any
			Messages []map[string]any
			Parallel bool `json:"parallel_tool_calls"`
		}
		if json.NewDecoder(r.Body).Decode(&v) != nil {
			t.Fatal("body")
		}
		if len(v.Tools) != 1 || v.Parallel || len(v.Messages) != 4 || v.Messages[2]["tool_calls"] == nil || v.Messages[3]["role"] != "tool" || v.Messages[3]["tool_call_id"] != "call_1" {
			t.Error("not native function transcript", v)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"next","type":"function","function":{"name":"speech_synthesize","arguments":"{\"text\":\"你好\"}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "key")
	os.WriteFile(file, []byte("synthetic-private-test-key"), 0600)
	c, _ := New(Config{BaseURL: server.URL, KeyFile: file})
	c.http.Transport = server.Client().Transport
	answer, err := c.ChatFunctions(context.Background(), "policy", []map[string]string{{"role": "user", "content": "current"}, {"role": "assistant", "content": `{"action":"tool","tool":"probe","arguments":{}}`}, {"role": "user", "content": `{"tool":"probe","result":"ok","source":"untrusted_tool_result"}`}}, []mcptools.Tool{{Name: "speech_synthesize", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	if err != nil || !json.Valid([]byte(answer)) {
		t.Fatal(answer, err)
	}
}

func TestNativeFunctionContentIsPreservedAsExecutionAcknowledgement(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"好，我来做一首～","tool_calls":[{"function":{"name":"music_local_start","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "key")
	os.WriteFile(file, []byte("synthetic-private-test-key"), 0600)
	client, _ := New(Config{BaseURL: server.URL, KeyFile: file})
	client.http = server.Client()
	answer, e := client.ChatFunctions(context.Background(), "policy", []map[string]string{{"role": "user", "content": "生成音乐"}}, nil)
	var result struct{ Progress string }
	json.Unmarshal([]byte(answer), &result)
	if e != nil || result.Progress != "好，我来做一首～" {
		t.Fatal("native acknowledgement lost", e, answer)
	}
}

func TestNativeResponseBatchAndSongArgumentsAreDecodedBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		args         []string
		wantCode     string
		wantBatch    bool
	}{
		{name: "two_functions", reason: "tool_calls", args: []string{`{"query":"first"}`, `{"query":"second"}`}, wantBatch: true},
		{name: "long_lyrics", reason: "tool_calls", args: []string{`{"lyrics":"` + strings.Repeat("歌", 1800) + `"}`}},
		{name: "invalid_second", reason: "tool_calls", args: []string{`{}`, `{"query":`}, wantCode: "model_tool_arguments_invalid"},
		{name: "oversized", reason: "tool_calls", args: []string{`{"text":"` + strings.Repeat("x", 17000) + `"}`}, wantCode: "model_tool_arguments_too_large"},
		{name: "truncated", reason: "length", wantCode: "model_output_token_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls := []any{}
				for _, args := range tc.args {
					calls = append(calls, map[string]any{"function": map[string]string{"name": "probe", "arguments": args}})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "", "tool_calls": calls}, "finish_reason": tc.reason}}})
			}))
			defer server.Close()
			file := filepath.Join(t.TempDir(), "key")
			os.WriteFile(file, []byte("synthetic-private-test-key"), 0600)
			client, _ := New(Config{BaseURL: server.URL, KeyFile: file})
			client.http = server.Client()
			answer, err := client.ChatFunctions(context.Background(), "policy", []map[string]string{{"role": "user", "content": "test"}}, nil)
			if tc.wantCode != "" {
				d, ok := ErrorDiagnostics(err)
				if !ok || d.Code != tc.wantCode {
					t.Fatal("wrong diagnosis", err)
				}
				return
			}
			if err != nil || !json.Valid([]byte(answer)) {
				t.Fatal(err, answer)
			}
			var result struct {
				Action string
				Calls  []json.RawMessage
			}
			_ = json.Unmarshal([]byte(answer), &result)
			if tc.wantBatch && (result.Action != "tool_batch" || len(result.Calls) != 2) {
				t.Fatal("calls lost", answer)
			}
		})
	}
}
