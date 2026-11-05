package admin

// Installed MCP interpreters receive credential-free protocol facts. The host
// retains transport declarations, caller authority and every actual RPC byte.
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
)

const externalMCPCapability = "external.mcp_request"

type externalMCPArguments struct {
	Operation       string          `json:"operation"`
	ServerID        string          `json:"server_id"`
	Tool            string          `json:"tool,omitempty"`
	Arguments       json.RawMessage `json:"arguments"`
	ArgumentsSHA256 string          `json:"arguments_sha256"`
	RequestSHA256   string          `json:"request_sha256"`
	Protocol        json.RawMessage `json:"protocol"`
}
type externalMCPAdminKey struct{}
type externalMCPAdminAuthority struct {
	Session         *speechAdminTestAuthority
	Operation, Tool string
}
type externalMCPAuthority struct {
	request   externalMCPArguments
	server    mcptools.ServerConfig
	hub       *mcptools.Hub
	cursor    string
	mediaKind string
	admin     *externalMCPAdminAuthority
}

var externalMCPName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func externalMCPSourceHash(settings mcpSettings, items []plugins.Installed) string {
	relevant := []plugins.Installed{}
	for _, item := range items {
		if item.Manifest.MCP != nil {
			relevant = append(relevant, item)
		}
	}
	raw, _ := json.Marshal([]any{settings, relevant})
	return namedSpeechSHA(raw)
}
func (m *Manager) externalMCPConfigurationHash() (string, error) {
	m.mu.Lock()
	settings := cloneMCP(m.mcp)
	m.mu.Unlock()
	return externalMCPSourceHash(settings, m.plugins.installed()), nil
}
func (p *permittedTools) externalMCPServers() []mcptools.ServerConfig {
	servers := append([]mcptools.ServerConfig{}, p.settings.Servers...)
	if p.runtime != nil {
		legacy, err := p.runtime.MCPServers()
		if err != nil {
			return nil
		}
		servers = append(servers, legacy...)
	}
	out := []mcptools.ServerConfig{}
	for _, server := range servers {
		server = server.Normalized()
		if server.Enabled {
			out = append(out, server)
		}
	}
	return out
}
func (g *externalMCPAuthority) authorization(ctx context.Context, p *externalHTTPPolicy) (string, error) {
	p.p.manager.mu.Lock()
	enabled := p.p.manager.mcp.Enabled
	p.p.manager.mu.Unlock()
	if !enabled || g.mediaKind != "" && !companion.MediaPermitted(p.p.manager.Config().Companion.Permissions, g.mediaKind) {
		return "", errors.New("external_mcp_disabled")
	}
	var authority string
	if g.admin != nil {
		if g.admin.Operation != g.request.Operation || g.admin.Operation == "call" && g.admin.Tool != g.server.ID+"__"+g.request.Tool {
			return "", errors.New("external_mcp_manual_scope_denied")
		}
		if err := g.admin.Session.valid(ctx, p.p.manager); err != nil {
			return "", err
		}
		authority = agentID(g.admin.Session.Account, g.admin.Session.Session, g.admin.Session.RequestID, g.admin.Operation, g.admin.Tool)
	} else {
		authority = p.p.externalHTTPAuthorization(plugins.ExternalProtocolTool{})
		if authority == "" {
			return "", errors.New("external_mcp_authorization_denied")
		}
	}
	raw, _ := json.Marshal([]any{p.p.manager.agent.config(), p.p.manager.Config().Companion.Permissions, enabled, g.request.RequestSHA256})
	return agentID(authority, namedSpeechSHA(raw)), nil
}
func (s *Server) externalMCPAdminContext(ctx context.Context, r *http.Request, operation, tool string) context.Context {
	original := s.speechPluginTestContext(r)
	session, _ := original.Value(speechAdminTestContextKey{}).(*speechAdminTestAuthority)
	return context.WithValue(ctx, externalMCPAdminKey{}, &externalMCPAdminAuthority{Session: session, Operation: operation, Tool: tool})
}
func (p *permittedTools) newExternalMCPPolicy(ctx context.Context, operation string, server mcptools.ServerConfig, tool, cursor string, args json.RawMessage) (*externalHTTPPolicy, error) {
	if operation != "list" && operation != "call" || !externalMCPName.MatchString(server.ID) || operation == "call" && (!externalMCPName.MatchString(tool) || len(server.Tools) > 0 && !slices.Contains(server.Tools, tool)) {
		return nil, errors.New("external_mcp_scope_denied")
	}
	if p.hub == nil || p.runtime == nil {
		return nil, errors.New("external_mcp_hub_missing")
	}
	legacy := p.mcpSourceHash
	current, err := p.manager.externalMCPConfigurationHash()
	if err != nil || legacy == "" || current != legacy {
		return nil, errors.New("external_mcp_configuration_changed")
	}
	trace, err := cloneProjectPluginTrace(p.trace)
	if err != nil {
		return nil, err
	}
	bound := &permittedTools{manager: p.manager, trace: trace}
	store, installed, revision, err := p.manager.externalPackageCapability(ctx, externalMCPCapability)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(args, &object) != nil || object == nil {
		return nil, toolInputError("external_mcp_arguments_invalid")
	}
	args = append(json.RawMessage(nil), args...)
	allowlist := append([]string{}, server.Tools...)
	protocol, _ := json.Marshal(map[string]any{"version": 1, "server_id": server.ID, "allowlist": allowlist})
	request := externalMCPArguments{Operation: operation, ServerID: server.ID, Tool: tool, Arguments: args, ArgumentsSHA256: namedSpeechSHA(args), Protocol: protocol}
	proof, _ := json.Marshal([]any{operation, server.ID, tool, cursor, request.ArgumentsSHA256, legacy})
	request.RequestSHA256 = namedSpeechSHA(proof)
	grant := &externalMCPAuthority{request: request, server: server, hub: p.hub, cursor: cursor, mediaKind: p.apiKinds[server.ID+"__"+tool]}
	grant.admin, _ = ctx.Value(externalMCPAdminKey{}).(*externalMCPAdminAuthority)
	policy := &externalHTTPPolicy{p: bound, store: store, installed: installed, configRevision: revision, legacyHash: legacy, mcp: grant}
	policy.arguments, _ = json.Marshal(request)
	authorization, err := policy.currentAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	epoch, err := p.manager.memory.RecallEpoch(ctx)
	if err != nil {
		return nil, err
	}
	sources := map[string]int{}
	if trace.Source != nil && trace.Source.EventID != "" {
		sources[trace.Source.EventID] = trace.Source.Revision
	}
	if trace.ReplyContext != nil {
		for id, rev := range trace.ReplyContext.SourceVersions {
			sources[id] = rev
		}
	}
	key := "external-mcp-" + agentID(trace.Account, trace.Thread, trace.Sender, trace.TraceID, trace.TaskID, request.RequestSHA256)
	if operation == "list" {
		key += "-" + randomToken()
	}
	policy.invocation = pluginhost.Invocation{Account: trace.Account, Thread: trace.Thread, Requester: trace.Sender, Purpose: "configured MCP " + operation + " " + server.ID, RequestKey: key, Authorization: authorization, EvidenceEpoch: epoch, SourceVersions: sources}
	if grant.admin != nil {
		policy.invocation = pluginhost.Invocation{Account: grant.admin.Session.Account, Thread: "host:external-mcp-" + operation, Requester: "administrator", Purpose: "manual MCP " + operation, RequestKey: "external-mcp-admin-" + agentID(authorization, request.RequestSHA256), Authorization: authorization}
	}
	return policy, nil
}
func (p *externalHTTPPolicy) mcpService(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var request struct {
		Operation     string `json:"operation"`
		RequestSHA256 string `json:"request_sha256"`
	}
	if method != "wechat/external_mcp" || p.requested || j.ID == "" || musicStrictJSON(raw, &request) != nil || request.Operation != p.mcp.request.Operation || request.RequestSHA256 != p.mcp.request.RequestSHA256 {
		return nil, errors.New("external_mcp_request_changed")
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	p.requested = true
	bound, stop := p.boundContext(ctx, j, cap)
	defer stop()
	scope := agentID(p.invocation.Account, p.invocation.Thread, p.invocation.Requester)
	if p.mcp.admin != nil {
		scope = agentID(scope, p.mcp.admin.Session.Session, p.mcp.admin.Session.RequestID)
	}
	bound = mcptools.WithSessionScope(bound, scope)
	rpcMethod := "tools/list"
	params := map[string]any{}
	if p.mcp.cursor != "" {
		params["cursor"] = p.mcp.cursor
	}
	if p.mcp.request.Operation == "call" {
		rpcMethod = "tools/call"
		params = map[string]any{"name": p.mcp.request.Tool, "arguments": p.mcp.request.Arguments}
	}
	encoded, _ := json.Marshal(params)
	reply, err := p.mcp.hub.Request(bound, p.mcp.server.ID, rpcMethod, encoded)
	if err != nil {
		if strings.Contains(err.Error(), "1 MiB") || strings.Contains(err.Error(), "MCP request too large") {
			return nil, errors.New("external_mcp_transport_frame_limit_1MiB_use_pages_or_media_references_outcome_unknown_do_not_resubmit")
		}
		return nil, errors.New("external_mcp_outcome_unknown_do_not_resubmit")
	}
	if err = p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	if err = p.captureReply(j, reply, "application/json"); err != nil {
		return nil, err
	}
	observation, err := externalMCPObservation(reply, p.body)
	if err != nil {
		return nil, err
	}
	p.observed = observation
	return append(json.RawMessage(nil), observation...), nil
}

type externalMCPMedia struct {
	Kind         string `json:"kind"`
	ContentIndex int    `json:"content_index"`
	HostMediaRef string `json:"host_media_ref"`
	MIMEType     string `json:"mime_type"`
}
type externalMCPWireResult struct {
	Observation externalHTTPBlobRef `json:"observation"`
	Tools       []mcptools.Tool     `json:"tools,omitempty"`
	Output      *struct {
		Text  string             `json:"text"`
		Media []externalMCPMedia `json:"media,omitempty"`
	} `json:"output,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func externalMCPObservation(raw []byte, ref externalHTTPBlobRef) (json.RawMessage, error) {
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
			block["host_media_ref"], _ = json.Marshal(namedSpeechSHA(data))
			block["content_index"], _ = json.Marshal(i)
		}
		result["content"], _ = json.Marshal(blocks)
	}
	return json.Marshal(map[string]any{"raw_result": result, "body_ref": ref})
}
func (p *externalHTTPPolicy) mcpResultProof(result externalMCPWireResult, data []byte, ref externalHTTPBlobRef) (plugins.Output, error) {
	denied := errors.New("external_mcp_result_changed")
	if result.Observation != ref || ref.SHA256 != namedSpeechSHA(data) || ref.Size != int64(len(data)) {
		return plugins.Output{}, denied
	}
	if p.mcp.request.Operation == "list" {
		var page struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(data, &page) != nil || result.Output != nil || result.IsError || result.NextCursor != page.NextCursor {
			return plugins.Output{}, denied
		}
		expected := []mcptools.Tool{}
		for _, tool := range page.Tools {
			if !externalMCPName.MatchString(tool.Name) || len(p.mcp.server.Tools) > 0 && !slices.Contains(p.mcp.server.Tools, tool.Name) {
				continue
			}
			schema := tool.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			expected = append(expected, mcptools.Tool{Name: p.mcp.server.ID + "__" + tool.Name, Description: tool.Description, InputSchema: schema})
		}
		actual, _ := json.Marshal(result.Tools)
		want, _ := json.Marshal(expected)
		if !sameJSON(actual, want) {
			return plugins.Output{}, denied
		}
		return plugins.Output{}, nil
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
		return plugins.Output{}, denied
	}
	output := plugins.Output{}
	texts := []string{}
	expected := []externalMCPMedia{}
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
			expected = append(expected, externalMCPMedia{Kind: kind, ContentIndex: i, HostMediaRef: namedSpeechSHA(decoded), MIMEType: block.MIMEType})
			output.Media = append(output.Media, plugins.MediaResult{Kind: kind, Data: decoded, MIMEType: block.MIMEType})
		default:
			return output, errors.New("external_mcp_content_type_requires_resource_reference")
		}
	}
	output.Text = strings.Join(texts, "\n")
	if output.Text != result.Output.Text || !slices.Equal(expected, result.Output.Media) {
		return plugins.Output{}, denied
	}
	return output, nil
}
func (p *externalHTTPPolicy) validateMCP(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	var result externalMCPWireResult
	if musicStrictJSON(raw, &result) != nil || !p.requested || len(p.observed) == 0 {
		return pluginruntime.VerifiedResult{}, errors.New("external_mcp_result_unverified")
	}
	data, err := p.readPrivateFile(j, "reply.bin", p.body, 1<<20)
	if err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if _, err = p.mcpResultProof(result, data, p.body); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	return p.savePhaseResult(j, raw)
}
func (p *externalHTTPPolicy) invokeMCP(ctx context.Context) (externalMCPWireResult, plugins.Output, error) {
	data, verified, checkpoint, err := p.invokePhase(ctx)
	if err != nil {
		return externalMCPWireResult{}, plugins.Output{}, err
	}
	var result externalMCPWireResult
	if musicStrictJSON(verified, &result) != nil {
		return result, plugins.Output{}, errors.New("external_mcp_checkpoint_invalid")
	}
	output, err := p.mcpResultProof(result, data, checkpoint.Body)
	return result, output, err
}
func (p *permittedTools) installedExternalMCPTools(ctx context.Context) ([]mcptools.Tool, error) {
	servers := p.externalMCPServers()
	if len(servers) == 0 {
		return nil, nil
	}
	if _, _, _, err := p.manager.externalPackageCapability(ctx, externalMCPCapability); err != nil {
		return nil, err
	}
	tools := []mcptools.Tool{}
	var failures []error
	for _, server := range servers {
		cursor := ""
		seen := map[string]bool{}
		for {
			policy, err := p.newExternalMCPPolicy(ctx, "list", server, "", cursor, nil)
			if err != nil {
				failures = append(failures, err)
				break
			}
			result, _, err := policy.invokeMCP(ctx)
			if err != nil {
				failures = append(failures, err)
				break
			}
			tools = append(tools, result.Tools...)
			if result.NextCursor == "" {
				break
			}
			if seen[result.NextCursor] {
				failures = append(failures, errors.New("external_mcp_cursor_repeated"))
				break
			}
			seen[result.NextCursor] = true
			cursor = result.NextCursor
		}
	}
	return tools, errors.Join(failures...)
}
func (p *permittedTools) callExternalMCP(ctx context.Context, name string, args json.RawMessage) (string, error) {
	serverID, tool, ok := strings.Cut(name, "__")
	if !ok {
		return "", errors.New("external_mcp_tool_invalid")
	}
	for _, server := range p.externalMCPServers() {
		if server.ID != serverID {
			continue
		}
		policy, err := p.newExternalMCPPolicy(ctx, "call", server, tool, "", args)
		if err != nil {
			return "", err
		}
		result, output, err := policy.invokeMCP(ctx)
		if err != nil {
			return "", err
		}
		if result.IsError {
			return output.Text, errors.New("external_mcp_tool_returned_error")
		}
		ctx = withOutputAuthority(ctx, func(check context.Context) error {
			return policy.Check(check, pluginhost.Job{Plugin: externalHTTPPluginID, Tool: externalMCPCapability, Context: policy.invocation, Arguments: policy.arguments}, pluginhost.Capability{})
		})
		if len(output.Media) > 0 {
			return p.stageOutput(ctx, output)
		}
		return output.Text, nil
	}
	return "", errors.New("external_mcp_server_not_registered")
}
func (p *permittedTools) externalMCPAdminListed(ctx context.Context, name string) bool {
	authority, _ := ctx.Value(externalMCPAdminKey{}).(*externalMCPAdminAuthority)
	if authority == nil || authority.Operation != "list" || authority.Session.valid(ctx, p.manager) != nil || p.apiNames[name] {
		return false
	}
	if _, _, _, err := p.manager.externalPackageCapability(ctx, externalMCPCapability); err != nil {
		return false
	}
	if kind := p.apiKinds[name]; kind != "" && !companion.MediaPermitted(p.manager.Config().Companion.Permissions, kind) {
		return false
	}
	serverID, tool, ok := strings.Cut(name, "__")
	if !ok || !externalMCPName.MatchString(tool) {
		return false
	}
	for _, server := range p.externalMCPServers() {
		if server.ID == serverID && (len(server.Tools) == 0 || slices.Contains(server.Tools, tool)) {
			return true
		}
	}
	return false
}
