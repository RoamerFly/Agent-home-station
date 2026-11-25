package plugins

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type ExternalHTTPBlobRef struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type ExternalHTTPObservation struct {
	ContentType string               `json:"content_type"`
	BodyBase64  string               `json:"body_base64,omitempty"`
	BinaryRef   *ExternalHTTPBlobRef `json:"binary_ref,omitempty"`
}
type ExternalHTTPWireMedia struct {
	MediaResult
	HostBinaryRef string `json:"host_binary_ref,omitempty"`
}
type ExternalHTTPWireOutput struct {
	Text  string                  `json:"text"`
	Media []ExternalHTTPWireMedia `json:"media,omitempty"`
}
type ExternalHTTPWireResult struct {
	Observation ExternalHTTPObservation `json:"observation"`
	Output      ExternalHTTPWireOutput  `json:"output"`
}

func ExternalHTTPValue(data any, path string) any {
	if path == "" {
		return nil
	}
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		object, ok := data.(map[string]any)
		if !ok {
			return nil
		}
		data = object[part]
	}
	return data
}
func ExternalHTTPText(value any) []string {
	if value == nil {
		return []string{""}
	}
	if text, ok := value.(string); ok {
		return []string{text}
	}
	encoded, _ := json.Marshal(value)
	return []string{string(encoded), fmt.Sprint(value)}
}

// Verify output provenance against the exact observed bytes and selected
// declaration. The package still owns extraction and protocol decisions.
func VerifyExternalHTTPOutput(tool ExternalProtocolTool, reply ExternalResponse, output Output) bool {
	data, err := base64.StdEncoding.Strict().DecodeString(reply.BodyBase64)
	if err != nil {
		return false
	}
	var value any
	if !ExternalHTTPIsJSON(data, reply.ContentType) {
		if strings.Contains(strings.ToLower(reply.ContentType), "json") || output.Text != "" || len(output.Media) != 1 {
			return false
		}
		media := output.Media[0]
		binary, err := base64.StdEncoding.Strict().DecodeString(media.Base64Data)
		return err == nil && media.URL == "" && media.Kind == tool.MediaKind && media.Kind != "" && media.MIMEType == reply.ContentType && media.Filename == "" && bytes.Equal(binary, data)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false
	}
	if !slices.Contains(ExternalHTTPText(ExternalHTTPValue(value, tool.TextPath)), output.Text) {
		return false
	}
	if len(output.Media) > 1 || tool.MediaURLPath != "" && len(output.Media) != 1 || tool.MediaURLPath == "" && len(output.Media) != 0 {
		return false
	}
	if len(output.Media) == 1 {
		media := output.Media[0]
		if media.Kind != tool.MediaKind || media.Kind == "" || media.Base64Data != "" || media.MIMEType != "" || media.Filename != "" || media.URL == "" {
			return false
		}
		expected, ok := ExternalHTTPValue(value, tool.MediaURLPath).(string)
		if !ok || expected != media.URL {
			return false
		}
	}
	return output.Text != "" || len(output.Media) > 0
}

func ExternalHTTPIsJSON(data []byte, contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "json") || json.Valid(data)
}
func MaterializeExternalHTTPOutput(tool ExternalProtocolTool, result ExternalHTTPWireResult, data []byte, body ExternalHTTPBlobRef, contentType string) (Output, error) {
	denied := errors.New("external_http_result_changed")
	if result.Observation.ContentType != contentType {
		return Output{}, denied
	}
	if !ExternalHTTPIsJSON(data, contentType) {
		if result.Observation.BinaryRef == nil || *result.Observation.BinaryRef != body || result.Observation.BodyBase64 != "" || result.Output.Text != "" || len(result.Output.Media) != 1 {
			return Output{}, denied
		}
		media := result.Output.Media[0]
		if media.HostBinaryRef != body.SHA256 || media.Kind != tool.MediaKind || media.Kind == "" || media.URL != "" || media.Base64Data != "" || media.MIMEType != "" || media.Filename != "" {
			return Output{}, denied
		}
		return Output{Media: []MediaResult{{Kind: media.Kind, Data: append([]byte(nil), data...), MIMEType: contentType}}}, nil
	}
	if result.Observation.BinaryRef != nil {
		return Output{}, denied
	}
	observed, err := base64.StdEncoding.Strict().DecodeString(result.Observation.BodyBase64)
	if err != nil || !bytes.Equal(data, observed) {
		return Output{}, denied
	}
	output := Output{Text: result.Output.Text}
	for _, media := range result.Output.Media {
		if media.HostBinaryRef != "" {
			return Output{}, denied
		}
		output.Media = append(output.Media, media.MediaResult)
	}
	if !VerifyExternalHTTPOutput(tool, ExternalResponse{ContentType: contentType, BodyBase64: result.Observation.BodyBase64}, output) {
		return Output{}, denied
	}
	return output, nil
}
