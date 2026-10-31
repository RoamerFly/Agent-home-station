// Offline MCP transport sample, not a production service or sandbox launcher.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	jobs := map[string]string{}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "invalid JSON"}})
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "synthetic-go-template", "version": "0.1.0"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			tools := []any{}
			for _, name := range []string{"artifact_sample", "health", "echo", "job_start", "job_status", "job_cancel"} {
				tools = append(tools, map[string]any{"name": name, "description": "Offline synthetic " + name, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]string{"type": "string"}, "id": map[string]string{"type": "string"}, "request_key": map[string]string{"type": "string"}}}})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			args := req.Params.Arguments
			value := map[string]any{}
			failed := false
			switch req.Params.Name {
			case "artifact_sample":
				value = map[string]any{"synthetic": true, "delivery": "not_submitted", "media": []any{map[string]string{"mime_type": "audio/wav", "filename": "synthetic.wav", "data_base64": "UklGRsQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YaAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}}
			case "health":
				value = map[string]any{"status": "ready", "synthetic": true}
			case "echo":
				value["text"] = args["text"]
			case "job_start":
				key, _ := args["request_key"].(string)
				if key == "" || len(key) > 128 {
					failed = true
					value["error"] = "request_key required"
				} else {
					if jobs[key] == "" {
						jobs[key] = "running"
					}
					value = map[string]any{"id": key, "state": jobs[key]}
				}
			case "job_status", "job_cancel":
				key, _ := args["id"].(string)
				state := jobs[key]
				if state == "" {
					state = "needs_review"
				} else if req.Params.Name == "job_cancel" {
					state = "cancelled"
					jobs[key] = state
				} else if state == "running" {
					state = "completed"
					jobs[key] = state
				}
				value = map[string]any{"id": key, "state": state}
			default:
				failed = true
				value["error"] = "unknown tool"
			}
			b, _ := json.Marshal(value)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": string(b)}}, "isError": failed}
		default:
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "unknown method"}})
			continue
		}
		if err := out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			fmt.Fprintln(os.Stderr, "output unavailable")
			return
		}
	}
}
