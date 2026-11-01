package mcptools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// PostPluginModelStream sends one approved request and normalizes a bounded
// OpenAI-compatible SSE completion. It does not select a model, modify the
// request body, retry, estimate usage or retain reasoning deltas.
func PostPluginModelStream(ctx context.Context, endpoint string, headers map[string]string, body json.RawMessage, maxBytes int64, timeout time.Duration) (json.RawMessage, error) {
	tr := newPublicTransport()
	defer tr.CloseIdleConnections()
	return postPluginModelStream(ctx, tr, endpoint, headers, body, maxBytes, timeout)
}

func postPluginModelStream(ctx context.Context, tr http.RoundTripper, endpoint string, headers map[string]string, body json.RawMessage, maxBytes int64, timeout time.Duration) (json.RawMessage, error) {
	req, err := pluginRouteRequest(ctx, endpoint, headers, body, maxBytes, timeout, 32<<20)
	if err != nil {
		return nil, err
	}
	// Do not provide the transport with a way to replay this POST, even when
	// a reused connection fails before it can write the request body.
	req.GetBody = nil
	if ctx.Err() != nil {
		return nil, &WebError{Code: "plugin_route_network_failed", Stage: "connection_or_headers"}
	}
	resp, err := pluginRouteClient(tr, timeout).Do(req)
	if err != nil {
		return nil, &WebError{Code: "plugin_route_network_failed", Stage: "connection_or_headers"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &WebError{Code: "plugin_route_http_rejected", Status: resp.StatusCode, Stage: "http_status"}
	}
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || contentType != "text/event-stream" {
		return nil, pluginStreamInvalid()
	}
	return readPluginModelStream(ctx, resp.Body, maxBytes)
}

type pluginStreamCompletion struct {
	content strings.Builder
	model   string
	finish  string
	usage   map[string]any
}

func pluginStreamInvalid() error {
	return &WebError{Code: "plugin_route_response_invalid", Stage: "response_body"}
}

func readPluginModelStream(ctx context.Context, source io.Reader, maxBytes int64) (json.RawMessage, error) {
	bounded := &io.LimitedReader{R: source, N: maxBytes + 1}
	reader := bufio.NewReader(bounded)
	completion := pluginStreamCompletion{}
	var data strings.Builder
	dataLines, eventType, firstLine := 0, "", true
	skipLF := false
	dispatch := func() (bool, error) {
		if eventType == "error" {
			return false, pluginStreamInvalid()
		}
		if dataLines == 0 {
			eventType = ""
			return false, nil
		}
		payload := strings.TrimSpace(data.String())
		data.Reset()
		dataLines, eventType = 0, ""
		if payload == "[DONE]" {
			if completion.finish == "" {
				return false, &WebError{Code: "plugin_route_stream_incomplete", Stage: "response_body"}
			}
			return true, nil
		}
		return false, completion.add([]byte(payload))
	}
	for {
		if ctx.Err() != nil {
			return nil, &WebError{Code: "plugin_route_response_failed", Stage: "response_body"}
		}
		line, readErr := pluginStreamLine(reader, &skipLF)
		if maxBytes+1-bounded.N > maxBytes {
			return nil, &WebError{Code: "response_too_large", Stage: "response_body"}
		}
		if len(line) > 0 || readErr == nil {
			if firstLine {
				line = strings.TrimPrefix(line, "\xef\xbb\xbf")
				firstLine = false
			}
			if !utf8.ValidString(line) {
				return nil, pluginStreamInvalid()
			}
			if line == "" {
				done, err := dispatch()
				if err != nil {
					return nil, err
				}
				if done {
					break
				}
			} else if !strings.HasPrefix(line, ":") {
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "data":
					if dataLines > 0 {
						data.WriteByte('\n')
					}
					data.WriteString(value)
					dataLines++
				case "event":
					eventType = value
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return nil, &WebError{Code: "plugin_route_response_failed", Stage: "response_body"}
			}
			if _, err := dispatch(); err != nil {
				return nil, err
			}
			break
		}
	}
	if ctx.Err() != nil {
		return nil, &WebError{Code: "plugin_route_response_failed", Stage: "response_body"}
	}
	if completion.finish == "" {
		return nil, &WebError{Code: "plugin_route_stream_incomplete", Stage: "response_body"}
	}
	result := map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": completion.content.String()}, "finish_reason": completion.finish}}}
	if completion.model != "" {
		result["model"] = completion.model
	}
	if completion.usage != nil {
		result["usage"] = completion.usage
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, pluginStreamInvalid()
	}
	if int64(len(encoded)) > maxBytes {
		return nil, &WebError{Code: "response_too_large", Stage: "response_body"}
	}
	return encoded, nil
}

// SSE permits LF, CRLF and bare CR. Do not peek after a CR: a terminal DONE
// event must return without waiting for another byte from a still-open stream.
func pluginStreamLine(reader *bufio.Reader, skipLF *bool) (string, error) {
	var line strings.Builder
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return line.String(), err
		}
		if *skipLF {
			*skipLF = false
			if value == '\n' {
				continue
			}
		}
		if value == '\r' {
			*skipLF = true
			return line.String(), nil
		}
		if value == '\n' {
			return line.String(), nil
		}
		line.WriteByte(value)
	}
}

func (c *pluginStreamCompletion) add(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := pluginStreamJSONValue(decoder)
	if err != nil {
		return pluginStreamInvalid()
	}
	if _, err = decoder.Token(); err != io.EOF {
		return pluginStreamInvalid()
	}
	chunk, ok := value.(map[string]any)
	if !ok || pluginStreamDisallowed(chunk) {
		return pluginStreamInvalid()
	}
	if model, present := chunk["model"]; present && model != nil {
		reported, ok := model.(string)
		if !ok || reported == "" || c.model != "" && c.model != reported {
			return pluginStreamInvalid()
		}
		c.model = reported
	}
	usageReported := false
	if usage, present := chunk["usage"]; present && usage != nil {
		reported, ok := usage.(map[string]any)
		if !ok || !pluginStreamUsage(reported) {
			return pluginStreamInvalid()
		}
		c.usage, usageReported = reported, true
	}
	choices, ok := chunk["choices"].([]any)
	if !ok || len(choices) > 1 {
		return pluginStreamInvalid()
	}
	if len(choices) == 0 {
		if !usageReported {
			return pluginStreamInvalid()
		}
		return nil
	}
	if c.finish != "" {
		return pluginStreamInvalid()
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return pluginStreamInvalid()
	}
	if index, present := choice["index"]; present {
		if number, ok := index.(json.Number); !ok || number.String() != "0" {
			return pluginStreamInvalid()
		}
	}
	if _, present := choice["message"]; present {
		return pluginStreamInvalid()
	}
	finish := ""
	if reason, present := choice["finish_reason"]; present && reason != nil {
		var ok bool
		finish, ok = reason.(string)
		if !ok || finish != "stop" && finish != "length" {
			return pluginStreamInvalid()
		}
	}
	delta, ok := choice["delta"].(map[string]any)
	if !ok && (choice["delta"] != nil || finish == "") {
		return pluginStreamInvalid()
	}
	if role, present := delta["role"]; present && role != "assistant" {
		return pluginStreamInvalid()
	}
	if content, present := delta["content"]; present && content != nil {
		text, ok := content.(string)
		if !ok {
			return pluginStreamInvalid()
		}
		c.content.WriteString(text)
	}
	c.finish = finish
	return nil
}

// Reject duplicate object fields instead of letting a later delta hide an
// earlier tool call. Unknown provider metadata is parsed but never returned.
func pluginStreamJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid object")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate field")
			}
			object[name], err = pluginStreamJSONValue(decoder)
			if err != nil {
				return nil, err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("invalid object")
		}
		return object, nil
	case json.Delim('['):
		array := []any{}
		for decoder.More() {
			value, err := pluginStreamJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("invalid array")
		}
		return array, nil
	}
	if _, delimiter := token.(json.Delim); delimiter {
		return nil, errors.New("invalid value")
	}
	return token, nil
}

func pluginStreamDisallowed(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "tool_calls" || key == "function_call" || key == "error" && child != nil {
				return true
			}
			if key != "reasoning" && key != "reasoning_content" && pluginStreamDisallowed(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if pluginStreamDisallowed(child) {
				return true
			}
		}
	}
	return false
}

func pluginStreamUsage(usage map[string]any) bool {
	for _, value := range usage {
		switch value := value.(type) {
		case json.Number:
			if strings.HasPrefix(value.String(), "-") {
				return false
			}
		case map[string]any:
			if !pluginStreamUsage(value) {
				return false
			}
		case nil:
		default:
			return false
		}
	}
	return true
}
