package plugins

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/strictjson"
)

var ExternalMCPName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func externalMCPDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type ExternalMCPMedia struct {
	Kind         string `json:"kind"`
	ContentIndex int    `json:"content_index"`
	HostMediaRef string `json:"host_media_ref"`
	MIMEType     string `json:"mime_type"`
}
type ExternalMCPWireResult struct {
	Observation ExternalHTTPBlobRef `json:"observation"`
	Tools       []mcptools.Tool     `json:"tools,omitempty"`
	Output      *struct {
		Text  string             `json:"text"`
		Media []ExternalMCPMedia `json:"media,omitempty"`
	} `json:"output,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func ExternalMCPObservation(raw []byte, ref ExternalHTTPBlobRef) (json.RawMessage, error) {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, errors.New("external_mcp_response_invalid")
	}
	if content, ok := result["content"]; ok {
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(content, &blocks) != nil {
			return nil, errors.New("external_mcp_content_invalid")
		}
		for i, block := range blocks {
			var kind string
			_ = json.Unmarshal(block["type"], &kind)
			if kind != "image" && kind != "audio" {
				continue
			}
			var encoded string
			if json.Unmarshal(block["data"], &encoded) != nil {
				return nil, errors.New("external_mcp_media_invalid")
			}
			data, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil {
				return nil, errors.New("external_mcp_media_invalid")
			}
			delete(block, "data")
			block["host_media_ref"], _ = json.Marshal(externalMCPDigest(data))
			block["content_index"], _ = json.Marshal(i)
		}
		result["content"], _ = json.Marshal(blocks)
	}
	return json.Marshal(map[string]any{"raw_result": result, "body_ref": ref})
}
func VerifyExternalMCPResult(server mcptools.ServerConfig, operation string, result ExternalMCPWireResult, data []byte, ref ExternalHTTPBlobRef) (Output, error) {
	denied := errors.New("external_mcp_result_changed")
	if result.Observation != ref || ref.SHA256 != externalMCPDigest(data) || ref.Size != int64(len(data)) {
		return Output{}, denied
	}
	if operation == "list" {
		var page struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(data, &page) != nil || result.Output != nil || result.IsError || result.NextCursor != page.NextCursor {
			return Output{}, denied
		}
		expected := []mcptools.Tool{}
		for _, tool := range page.Tools {
			if !ExternalMCPName.MatchString(tool.Name) || len(server.Tools) > 0 && !slices.Contains(server.Tools, tool.Name) {
				continue
			}
			schema := tool.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			expected = append(expected, mcptools.Tool{Name: server.ID + "__" + tool.Name, Description: tool.Description, InputSchema: schema})
		}
		actual, _ := json.Marshal(result.Tools)
		if !strictjson.Matches(actual, expected) {
			return Output{}, denied
		}
		return Output{}, nil
	}
	var reply struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MIMEType string `json:"mimeType"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(data, &reply) != nil || result.Output == nil || len(result.Tools) != 0 || result.NextCursor != "" || result.IsError != reply.IsError {
		return Output{}, denied
	}
	output := Output{}
	texts := []string{}
	expected := []ExternalMCPMedia{}
	for i, block := range reply.Content {
		switch block.Type {
		case "text":
			texts = append(texts, block.Text)
		case "image", "audio":
			decoded, err := base64.StdEncoding.Strict().DecodeString(block.Data)
			if err != nil {
				return output, denied
			}
			kind := "image"
			if block.Type == "audio" {
				kind = "voice"
			}
			expected = append(expected, ExternalMCPMedia{Kind: kind, ContentIndex: i, HostMediaRef: externalMCPDigest(decoded), MIMEType: block.MIMEType})
			output.Media = append(output.Media, MediaResult{Kind: kind, Data: decoded, MIMEType: block.MIMEType})
		default:
			return output, errors.New("external_mcp_content_type_requires_resource_reference")
		}
	}
	output.Text = strings.Join(texts, "\n")
	if output.Text != result.Output.Text || !slices.Equal(expected, result.Output.Media) {
		return Output{}, denied
	}
	return output, nil
}
