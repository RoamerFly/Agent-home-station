package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// Request performs one host-bound MCP operation without interpreting or
// truncating its JSON result. Discovery returns one original server page;
// callers project its untrusted declarations against their own schema. Calls
// always enforce the configured server's tool allowlist. This transport API
// does not grant source/actor authorization or an unrestricted stdio launcher.
func (h *Hub) Request(ctx context.Context, serverID, method string, params json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if method != "tools/list" && method != "tools/call" {
		return nil, errors.New("MCP bridge method not permitted")
	}
	var server *ServerConfig
	for index := range h.servers {
		if h.servers[index].ID == serverID && h.servers[index].Enabled {
			server = &h.servers[index]
			break
		}
	}
	if server == nil {
		return nil, errors.New("MCP configured server unavailable")
	}
	object, err := bridgeRequestObject(params)
	if err != nil {
		return nil, err
	}
	var request any = params
	if method == "tools/list" {
		if len(object) > 1 {
			return nil, errors.New("invalid MCP tools/list params")
		}
		page := map[string]any{}
		for key, value := range object {
			cursor, ok := value.(string)
			if key != "cursor" || !ok {
				return nil, errors.New("invalid MCP tools/list params")
			}
			page["cursor"] = cursor
		}
		// scopedRequest recognizes the map's cursor and therefore cannot
		// mistake a later raw-JSON page for cached first-page discovery.
		request = page
	} else {
		name, ok := object["name"].(string)
		if !ok || !toolName.MatchString(name) || !allowed(*server, name) {
			return nil, errors.New("MCP tool is not allowed")
		}
		for key, value := range object {
			switch key {
			case "name":
			case "arguments":
				if _, ok := value.(map[string]any); !ok {
					return nil, errors.New("MCP tool arguments must be an object")
				}
			default:
				return nil, errors.New("invalid MCP tools/call params")
			}
		}
		// Keep the original RawMessage. Decoding arguments into float64 and
		// remarshal would change large integers and decimal/exponent lexemes.
	}
	var result json.RawMessage
	if err = h.request(ctx, *server, method, request, &result); err != nil {
		return nil, err
	}
	if !json.Valid(result) {
		return nil, errors.New("MCP response result missing or invalid")
	}
	return append(json.RawMessage(nil), result...), nil
}

func bridgeRequestObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) > maxFrame {
		return nil, errors.New("MCP request params exceed 1 MiB transport frame")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := pluginStreamJSONValue(decoder)
	if err != nil {
		return nil, errors.New("invalid MCP request JSON")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("MCP request must contain one JSON object")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("MCP request params must be an object")
	}
	return object, nil
}
