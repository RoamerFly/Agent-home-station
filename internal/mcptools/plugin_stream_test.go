package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func pluginSSETransport(stream string, calls *int) pluginRouteTransport {
	return func(r *http.Request) (*http.Response, error) {
		*calls = *calls + 1
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(stream)), Request: r}, nil
	}
}

func pluginSSEChunk(content, finish string) string {
	chunk := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": content}, "finish_reason": nil}}}
	if finish != "" {
		chunk["choices"].([]any)[0].(map[string]any)["finish_reason"] = finish
	}
	raw, _ := json.Marshal(chunk)
	return "data: " + string(raw) + "\n\n"
}

func TestPluginModelStreamNormalizesOneChoiceAndReportedUsage(t *testing.T) {
	stream := ": heartbeat\r\n\r\ndata: {\"model\":\"reported-model\",\"usage\":{\"completion_tokens\":2},\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"synthetic-private-reasoning\"},\"finish_reason\":null}]}\r\n\r\n" +
		pluginSSEChunk("你好", "") + pluginSSEChunk(" world", "stop") +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":31,\"completion_tokens\":7,\"completion_tokens_details\":{\"reasoning_tokens\":3},\"provider_seconds\":1.5}}\n\ndata: [DONE]\n\n"
	calls := 0
	tr := pluginSSETransport(stream, &calls)
	body := json.RawMessage(`{"model":"bound-by-caller","stream":true}`)
	checked := pluginRouteTransport(func(r *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != string(body) || r.Method != "POST" || r.GetBody != nil || r.URL.String() != "https://provider.example/approved" || r.Header.Get("Authorization") != "Bearer synthetic-secret" || r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("approved stream request changed")
		}
		return tr.RoundTrip(r)
	})
	result, err := postPluginModelStream(t.Context(), checked, "https://provider.example/approved", map[string]string{"Authorization": "Bearer synthetic-secret", "Accept": "text/event-stream"}, body, 4096, time.Second)
	if err != nil || calls != 1 || strings.Contains(string(result), "synthetic-private-reasoning") || strings.Contains(string(result), "synthetic-secret") {
		t.Fatal("normalization failed or leaked private fields", calls, string(result), err)
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(result, &out) != nil || out.Model != "reported-model" || len(out.Choices) != 1 || out.Choices[0].Message.Content != "你好 world" || out.Choices[0].Finish != "stop" || string(out.Usage["prompt_tokens"]) != "31" || string(out.Usage["completion_tokens"]) != "7" || string(out.Usage["provider_seconds"]) != "1.5" || out.Usage["total_tokens"] != nil {
		t.Fatal("reported usage/content changed or totals were invented", string(result))
	}
}

func TestPluginModelStreamAcceptsFinishedEOFAndDoesNotInventUsage(t *testing.T) {
	for _, finish := range []string{"stop", "length"} {
		for _, suffix := range []string{"", "data: [DONE]\n\n"} {
			t.Run(finish+suffix, func(t *testing.T) {
				calls := 0
				result, err := postPluginModelStream(t.Context(), pluginSSETransport(pluginSSEChunk("bounded content", finish)+suffix, &calls), "https://provider.example/api", nil, []byte(`{}`), 2048, time.Second)
				if err != nil || calls != 1 || strings.Contains(string(result), `"usage"`) || strings.Contains(string(result), `"model"`) || !strings.Contains(string(result), `"finish_reason":"`+finish+`"`) {
					t.Fatal(string(result), err, calls)
				}
			})
		}
	}
}

func TestPluginModelStreamAcceptsSSELineEndings(t *testing.T) {
	for name, separator := range map[string]string{"LF": "\n", "CRLF": "\r\n", "CR": "\r"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			stream := strings.ReplaceAll(pluginSSEChunk("line endings", "stop")+"data: [DONE]\n\n", "\n", separator)
			result, err := postPluginModelStream(t.Context(), pluginSSETransport(stream, &calls), "https://provider.example/api", nil, []byte(`{}`), 1024, time.Second)
			if err != nil || calls != 1 || !strings.Contains(string(result), "line endings") {
				t.Fatal("valid SSE line ending rejected", string(result), err)
			}
		})
	}
}

type pluginSSEMustStopBody struct {
	*strings.Reader
	closed bool
}

func (r *pluginSSEMustStopBody) Read(data []byte) (int, error) {
	if r.Reader.Len() == 0 {
		return 0, errors.New("stream was read after DONE")
	}
	return r.Reader.Read(data)
}
func (r *pluginSSEMustStopBody) Close() error { r.closed = true; return nil }

func TestPluginModelStreamDoneDoesNotWaitForEOFAndHandlesMultilineData(t *testing.T) {
	stream := "\xef\xbb\xbfdata: {\"choices\":[\ndata: {\"delta\":{\"content\":\"multiline\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	body := &pluginSSEMustStopBody{Reader: strings.NewReader(stream)}
	calls := 0
	tr := pluginRouteTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body, Request: r}, nil
	})
	result, err := postPluginModelStream(t.Context(), tr, "https://provider.example/api", nil, []byte(`{}`), 1024, time.Second)
	if err != nil || calls != 1 || !body.closed || !strings.Contains(string(result), "multiline") {
		t.Fatal("DONE did not terminate and close response", string(result), calls, err)
	}
	// EOF may terminate the final event without a newline or blank separator.
	calls = 0
	result, err = postPluginModelStream(t.Context(), pluginSSETransport(strings.TrimRight(pluginSSEChunk("last event", "stop"), "\n"), &calls), "https://provider.example/api", nil, []byte(`{}`), 1024, time.Second)
	if err != nil || calls != 1 || !strings.Contains(string(result), "last event") {
		t.Fatal("finished final event at EOF was rejected", string(result), err)
	}
}

func TestPluginModelStreamRejectsInvalidOrIncompleteReplies(t *testing.T) {
	for name, stream := range map[string]string{
		"no_finish":            pluginSSEChunk("unfinished", "") + "data: [DONE]\n\n",
		"no_finish_eof":        pluginSSEChunk("unfinished", ""),
		"done_only":            "data: [DONE]\n\n",
		"malformed_json":       "data: {invalid}\n\n",
		"duplicate_delta":      "data: {\"choices\":[{\"delta\":{\"tool_calls\":[]},\"delta\":{\"content\":\"hidden tool\"},\"finish_reason\":\"stop\"}]}\n\n",
		"tool_delta":           "data: {\"choices\":[{\"delta\":{\"content\":\"answer\",\"tool_calls\":[]},\"finish_reason\":\"stop\"}]}\n\n",
		"function_delta":       "data: {\"choices\":[{\"delta\":{\"function_call\":{}},\"finish_reason\":\"stop\"}]}\n\n",
		"tool_finish":          "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
		"provider_error":       "data: {\"error\":{\"message\":\"synthetic-private-secret\"}}\n\n",
		"provider_error_event": "event: error\ndata: {\"message\":\"synthetic-private-secret\"}\n\n",
		"multiple_choices":     "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"different_index":      "data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"other\"},\"finish_reason\":\"stop\"}]}\n\n",
		"message_in_stream":    "data: {\"choices\":[{\"message\":{\"content\":\"not a delta\"},\"finish_reason\":\"stop\"}]}\n\n",
		"data_after_finish":    pluginSSEChunk("finished", "stop") + pluginSSEChunk("more", ""),
		"model_changed":        "data: {\"model\":\"first\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"model\":\"second\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"usage_text":           pluginSSEChunk("finished", "stop") + "data: {\"choices\":[],\"usage\":{\"private_reasoning\":\"synthetic-private-secret\"}}\n\n",
		"invalid_utf8":         "data: " + string([]byte{255}) + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			result, err := postPluginModelStream(t.Context(), pluginSSETransport(stream, &calls), "https://provider.example/api", nil, []byte(`{}`), 4096, time.Second)
			if err == nil || len(result) != 0 || calls != 1 || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatal("invalid stream accepted/retried/leaked", calls, string(result), err)
			}
		})
	}
}

func TestPluginModelStreamRejectsRequestAndRedirectWithoutCredentialLeak(t *testing.T) {
	for _, endpoint := range []string{"http://provider.example/api", "https://user:secret@provider.example/api", "https://provider.example/api?secret=query", "https://provider.example/api#fragment"} {
		calls := 0
		_, err := postPluginModelStream(t.Context(), pluginSSETransport("", &calls), endpoint, nil, []byte(`{}`), 1024, time.Second)
		if err == nil || calls != 0 || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe request reached transport", endpoint, calls, err)
		}
	}
	for _, headers := range []map[string]string{{"X-Unknown": "synthetic-secret"}, {"Authorization": "synthetic-secret\r\nInjected: yes"}} {
		calls := 0
		_, err := postPluginModelStream(t.Context(), pluginSSETransport("", &calls), "https://provider.example/api", headers, []byte(`{}`), 1024, time.Second)
		if err == nil || calls != 0 || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("invalid headers reached transport", calls, err)
		}
	}
	for _, status := range []int{301, 302, 307, 308, 401, 500} {
		calls := 0
		tr := pluginRouteTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://steal.example/credential"}}, Body: io.NopCloser(strings.NewReader("synthetic-private-error")), Request: r}, nil
		})
		_, err := postPluginModelStream(t.Context(), tr, "https://provider.example/api", map[string]string{"Authorization": "Bearer synthetic-secret"}, []byte(`{}`), 1024, time.Second)
		if err == nil || calls != 1 || strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "steal.example") {
			t.Fatal("redirect/retry/error leak", calls, err)
		}
	}
	// Production transport rejects literal private IPs before any TCP dial.
	for _, endpoint := range []string{"https://127.0.0.1/api", "https://[::1]/api"} {
		if _, err := PostPluginModelStream(t.Context(), endpoint, nil, []byte(`{}`), 1024, time.Second); err == nil {
			t.Fatal("private destination accepted", endpoint)
		}
	}
}

func TestPluginModelStreamBoundsBodyWireAndNormalizedReply(t *testing.T) {
	for _, test := range []struct {
		name, stream string
		limit        int64
	}{
		{"wire_reasoning", "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"" + strings.Repeat("x", 4096) + "\"},\"finish_reason\":\"stop\"}]}\n\n", 1024},
		{"normalized_encoding", strings.ReplaceAll(pluginSSEChunk(strings.Repeat("<", 50), "stop"), `\u003c`, "<"), 240},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			result, err := postPluginModelStream(t.Context(), pluginSSETransport(test.stream, &calls), "https://provider.example/api", nil, []byte(`{}`), test.limit, time.Second)
			var failure *WebError
			if !errors.As(err, &failure) || failure.Code != "response_too_large" || len(result) != 0 || calls != 1 {
				t.Fatal("stream limit bypassed", calls, err)
			}
		})
	}
	for _, limit := range []int64{0, (4 << 20) + 1} {
		calls := 0
		if _, err := postPluginModelStream(t.Context(), pluginSSETransport("", &calls), "https://provider.example/api", nil, []byte(`{}`), limit, time.Second); err == nil || calls != 0 {
			t.Fatal("invalid reply bound accepted", limit)
		}
	}
	for _, bytes := range []int{2 << 20, 32 << 20} {
		calls := 0
		body := json.RawMessage(`{"audio":"` + strings.Repeat("a", bytes) + `"}`)
		_, err := postPluginModelStream(t.Context(), pluginSSETransport(pluginSSEChunk("ok", "stop"), &calls), "https://provider.example/api", nil, body, 1024, time.Second)
		if bytes == 2<<20 && (err != nil || calls != 1) || bytes == 32<<20 && (err == nil || calls != 0) {
			t.Fatal("model body bound changed", bytes, calls, err)
		}
	}
}

type pluginSSEBlockedBody struct{ ctx context.Context }

func (r *pluginSSEBlockedBody) Read([]byte) (int, error) { <-r.ctx.Done(); return 0, r.ctx.Err() }
func (*pluginSSEBlockedBody) Close() error               { return nil }

func TestPluginModelStreamCancellationTimeoutAndReadErrorsDoNotRetry(t *testing.T) {
	for _, mode := range []string{"cancelled_before", "cancelled_body", "timeout_body", "network_failure", "wrong_content_type"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			calls := 0
			if mode == "cancelled_before" {
				cancel()
			}
			tr := pluginRouteTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if mode == "network_failure" {
					return nil, errors.New("synthetic-private-network-error")
				}
				if mode == "cancelled_body" {
					cancel()
				}
				if mode == "wrong_content_type" {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"secret":"synthetic-private"}`)), Request: r}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &pluginSSEBlockedBody{ctx: r.Context()}, Request: r}, nil
			})
			start := time.Now()
			result, err := postPluginModelStream(ctx, tr, "https://provider.example/api", nil, []byte(`{}`), 1024, 25*time.Millisecond)
			wantedCalls := 1
			if mode == "cancelled_before" {
				wantedCalls = 0
			}
			if err == nil || len(result) != 0 || calls != wantedCalls || time.Since(start) > time.Second || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatal("cancelled/timed-out stream retried, leaked or returned data", calls, string(result), err)
			}
		})
	}
}
