package plugins

// ExternalBridge is the narrow host half of the external-tools stdio protocol.
// It deliberately executes one already-declared HTTP phase only; task order,
// polling decisions and result extraction belong to the isolated runtime.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
)

type ExternalProtocol struct {
	Version int                    `json:"version"`
	Tools   []ExternalProtocolTool `json:"tools"`
}
type ExternalProtocolTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	TextPath     string          `json:"text_path,omitempty"`
	MediaURLPath string          `json:"media_url_path,omitempty"`
	MediaKind    string          `json:"media_kind,omitempty"`
	Poll         *ExternalPoll   `json:"poll,omitempty"`
	Job          *ExternalJob    `json:"job,omitempty"`
	Fingerprint  string          `json:"fingerprint,omitempty"`
}
type ExternalPoll struct {
	StatusPath   string   `json:"status_path"`
	DoneValues   []string `json:"done_values"`
	FailedValues []string `json:"failed_values,omitempty"`
	TextPath     string   `json:"text_path,omitempty"`
	MediaURLPath string   `json:"media_url_path,omitempty"`
	MediaKind    string   `json:"media_kind,omitempty"`
}
type ExternalJob struct {
	IDPath string `json:"id_path"`
}

type ExternalRequest struct {
	Phase     string          `json:"phase"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Ticket    JobTicket       `json:"ticket,omitempty"`
}
type ExternalResponse struct {
	ContentType string `json:"content_type"`
	BodyBase64  string `json:"body_base64"`
}

type ExternalBridge struct{ runtime *Runtime }

func NewExternalBridge(items []Installed) (*ExternalBridge, error) {
	r, err := New(items)
	if err != nil {
		return nil, err
	}
	return &ExternalBridge{runtime: r}, nil
}

// Protocol is credential-free metadata for the isolated runtime.  In
// particular it intentionally omits URL, headers, templates and Secrets.
func (b *ExternalBridge) Protocol() ExternalProtocol {
	out := ExternalProtocol{Version: 1}
	for name, tool := range b.runtime.tools {
		// The fixed external.http_request capability is synchronous only. Keep
		// legacy async declarations out of this projection so they remain on
		// their existing durable path until an equally durable migration exists.
		if tool.Job != nil || tool.Poll != nil {
			continue
		}
		item := ExternalProtocolTool{Name: name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), tool.InputSchema...), TextPath: tool.TextPath, MediaURLPath: tool.MediaURLPath, MediaKind: tool.MediaKind, Fingerprint: toolFingerprint(tool)}
		out.Tools = append(out.Tools, item)
	}
	sort.Slice(out.Tools, func(i, j int) bool { return out.Tools[i].Name < out.Tools[j].Name })
	return out
}

func (b *ExternalBridge) tool(name string, ticket JobTicket, requireJob bool) (APITool, Installed, error) {
	if requireJob {
		return b.runtime.jobTool(ticket)
	}
	t, ok := b.runtime.tools[name]
	if !ok || t.Job != nil {
		return APITool{}, Installed{}, errors.New("external_tool_unavailable")
	}
	parts := strings.SplitN(name, "__", 2)
	if len(parts) != 2 {
		return APITool{}, Installed{}, errors.New("external_tool_unavailable")
	}
	return t, b.runtime.installed[parts[0]], nil
}
func externalArguments(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > maxArgs {
		return nil, errors.New("external_arguments_invalid")
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil || out == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("external_arguments_invalid")
	}
	return out, nil
}
func externalReply(body []byte, contentType string) ExternalResponse {
	return ExternalResponse{ContentType: contentType, BodyBase64: base64.StdEncoding.EncodeToString(body)}
}

// Execute never uses Runtime.Call/StartJob/PollJob/CancelJob: those methods
// are legacy orchestration.  This is a single exact request after the caller
// has bound this tool/phase to an authorized durable invocation.
func (b *ExternalBridge) Execute(ctx context.Context, in ExternalRequest) (ExternalResponse, error) {
	args, err := externalArguments(in.Arguments)
	if err != nil {
		return ExternalResponse{}, err
	}
	switch in.Phase {
	case "call":
		t, installed, e := b.tool(in.Tool, JobTicket{}, false)
		if e != nil {
			return ExternalResponse{}, e
		}
		body, e := renderBody(t.BodyTemplate, installed.Secrets, args, nil)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		data, typ, e := request(ctx, t.URL, t.Method, t.Headers, body, installed.Secrets, args, nil, t.TimeoutSeconds)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		return externalReply(data, typ), nil
	case "start":
		t, installed, e := b.tool(in.Tool, in.Ticket, true)
		if e != nil || in.Ticket.Tool != in.Tool || in.Ticket.State != "submitting" || in.Ticket.RemoteID != "" {
			return ExternalResponse{}, errors.New("external_ticket_invalid")
		}
		body, e := renderBody(t.BodyTemplate, installed.Secrets, args, nil)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		data, typ, e := request(ctx, t.URL, t.Method, t.Headers, body, installed.Secrets, args, nil, t.TimeoutSeconds)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		return externalReply(data, typ), nil
	case "poll":
		t, installed, e := b.tool(in.Tool, in.Ticket, true)
		if e != nil || in.Ticket.Tool != in.Tool || t.Poll == nil || !jobIDPattern.MatchString(in.Ticket.RemoteID) || in.Ticket.RemoteID == "." || in.Ticket.RemoteID == ".." {
			return ExternalResponse{}, errors.New("external_ticket_invalid")
		}
		seed := jobSeed(t.Job.IDPath, in.Ticket.RemoteID)
		target, e := renderURL(t.Poll.URL, installed.Secrets, nil, seed)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		headers := followHeaders(t.URL, target, t.Poll.Headers, t.Headers)
		data, typ, e := request(ctx, target, http.MethodGet, headers, nil, installed.Secrets, nil, nil, t.TimeoutSeconds)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		return externalReply(data, typ), nil
	case "cancel":
		t, installed, e := b.tool(in.Tool, in.Ticket, true)
		if e != nil || in.Ticket.Tool != in.Tool || t.Job.Cancel == nil || !jobIDPattern.MatchString(in.Ticket.RemoteID) || in.Ticket.RemoteID == "." || in.Ticket.RemoteID == ".." {
			return ExternalResponse{}, errors.New("external_ticket_invalid")
		}
		seed := jobSeed(t.Job.IDPath, in.Ticket.RemoteID)
		target, e := renderURL(t.Job.Cancel.URL, installed.Secrets, nil, seed)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		body, e := renderBody(t.Job.Cancel.BodyTemplate, installed.Secrets, nil, seed)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		method := t.Job.Cancel.Method
		if method == "" {
			method = http.MethodPost
		}
		data, typ, e := request(ctx, target, method, followHeaders(t.URL, target, t.Job.Cancel.Headers, t.Headers), body, installed.Secrets, nil, nil, t.TimeoutSeconds)
		if e != nil {
			return ExternalResponse{}, safeErr(e)
		}
		return externalReply(data, typ), nil
	default:
		return ExternalResponse{}, errors.New("external_phase_unavailable")
	}
}
