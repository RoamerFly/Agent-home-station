package station

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/stationruntime"
	"wechat-bot/internal/strictjson"
	"wechat-bot/internal/workspacefs"
)

const (
	localExternalMCPCap     = "external.mcp_request"
	localExternalMCPServers = "station_external_mcp_servers"
	localExternalMCPList    = "station_external_mcp_list"
	localExternalMCPCall    = "station_external_mcp_call"
)

type localExternalRegistrationData struct {
	Connections []plugins.Installed     `json:"connections"`
	MCPServers  []mcptools.ServerConfig `json:"mcp_servers"`
}

func parseLocalExternalRegistration(raw []byte) (localExternalRegistrationData, error) {
	var config localExternalRegistrationData
	if _, err := strictjson.Decode(raw); err != nil || strictjson.DecodeKnownFields(raw, &config) != nil ||
		config.Connections == nil && config.MCPServers == nil {
		return config, errors.New("外部工具登记格式无效")
	}
	return config, nil
}

func registeredStationMCPServers(config localExternalRegistrationData) ([]mcptools.ServerConfig, error) {
	seen := make(map[string]bool)
	servers := make([]mcptools.ServerConfig, 0, len(config.MCPServers))
	for _, s := range config.MCPServers {
		s = s.Normalized()
		if seen[s.ID] || s.Command != "" || len(s.Args) != 0 || len(s.Env) != 0 || s.Transport == "stdio" && s.PluginID == "" {
			return nil, errors.New("MCP 登记重复，或本机连接没有使用已登记的工具包")
		}
		seen[s.ID] = true
		if s.Enabled {
			servers = append(servers, s)
		}
	}
	// New validates declarations without contacting a server.
	hub, err := mcptools.New(servers)
	if err != nil {
		return nil, errors.New("工作站 MCP 登记无效")
	}
	_ = hub.Close()
	return servers, nil
}

func (t *localToolSet) localExternalMCPTools(ctx context.Context) []mcptools.Tool {
	host := t.localExternalHost(ctx)
	if host == nil {
		return nil
	}
	if i, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage}); err != nil || !slices.Contains(i.Permissions, "network") {
		return nil
	}
	raw, err := t.projectTools.externalRegistration()
	if err != nil {
		return nil
	}
	config, err := parseLocalExternalRegistration(raw)
	if err != nil {
		return nil
	}
	servers, err := registeredStationMCPServers(config)
	if err != nil || len(servers) == 0 {
		return nil
	}
	return []mcptools.Tool{
		mcptools.NewTool(localExternalMCPServers, "列出主人已启用的 MCP 连接。只返回名称、连接编号和传输类型，不联系服务。", `{"type":"object","properties":{},"additionalProperties":false}`),
		mcptools.NewTool(localExternalMCPList, "读取指定 MCP 服务的一页已授权工具目录。使用 next_cursor 或同一 cursor 下的 next_offset 继续。较大的参数声明可用 schema_tool 和 text_offset 在同一目录页续读。", `{"type":"object","properties":{"server_id":{"type":"string"},"cursor":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1},"schema_tool":{"type":"string"},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1}},"required":["server_id"],"additionalProperties":false}`),
		mcptools.NewTool(localExternalMCPCall, "调用已登记 MCP 连接中的获准工具，使用目录列出的工具名。正文可按字符续读并复用原调用。结果未知时先核对原服务。", `{"type":"object","properties":{"server_id":{"type":"string"},"tool":{"type":"string"},"arguments":{"type":"object"},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1}},"required":["server_id","tool","arguments"],"additionalProperties":false}`),
	}
}

type localExternalMCPInput struct {
	Server     string          `json:"server_id"`
	Cursor     string          `json:"cursor"`
	PageOffset int             `json:"offset"`
	PageLimit  int             `json:"limit"`
	SchemaTool string          `json:"schema_tool"`
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	TextOffset int             `json:"text_offset"`
	TextLimit  int             `json:"text_limit"`
}

type localExternalMCPRequest struct {
	Operation       string          `json:"operation"`
	ServerID        string          `json:"server_id"`
	Tool            string          `json:"tool,omitempty"`
	Arguments       json.RawMessage `json:"arguments"`
	ArgumentsSHA256 string          `json:"arguments_sha256"`
	RequestSHA256   string          `json:"request_sha256"`
	Protocol        json.RawMessage `json:"protocol"`
}

func (t *localToolSet) callLocalExternalMCP(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	host := t.localExternalHost(ctx)
	if host == nil {
		return "", errors.New("当前请求没有工作站外部工具授权")
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return "", errors.New("外部工具连接未启用或缺少联网授权")
	}
	registration, err := t.projectTools.externalRegistration()
	if err != nil {
		return "", errors.New("工作站外部工具登记不可读")
	}
	config, err := parseLocalExternalRegistration(registration)
	if err != nil {
		return "", err
	}
	servers, err := registeredStationMCPServers(config)
	if err != nil {
		return "", err
	}
	if len(raw) > 1<<20 {
		return "", mcptools.ArgumentError("$", "MCP 请求超过 1 MiB，请分页处理或使用素材引用")
	}
	if name == localExternalMCPServers {
		if !strictjson.Matches(raw, map[string]any{}) {
			return "", mcptools.ArgumentError("$", "MCP 连接列表无需参数")
		}
		views := make([]map[string]string, 0, len(servers))
		for _, server := range servers {
			views = append(views, map[string]string{"server_id": server.ID, "name": server.Name, "transport": server.Transport})
		}
		return localProcessJSON(map[string]any{"servers": views})
	}
	var input localExternalMCPInput
	if _, err := strictjson.Decode(raw); err != nil || strictjson.DecodeKnownFields(raw, &input) != nil || input.TextOffset < 0 || input.TextLimit < 0 || input.PageOffset < 0 || input.PageLimit < 0 {
		return "", mcptools.ArgumentError("$", "MCP 工具参数或分页范围无效")
	}
	i := slices.IndexFunc(servers, func(c mcptools.ServerConfig) bool { return c.ID == input.Server })
	if i < 0 {
		return "", mcptools.ArgumentError("$.server_id", "该 MCP 连接未启用")
	}
	server := servers[i]
	operation, target, tool := "list", server.ID, ""
	args := json.RawMessage(`{}`)
	if name == localExternalMCPCall {
		operation, target = "call", input.Tool
		tool = strings.TrimPrefix(input.Tool, server.ID+"__")
		if !plugins.ExternalMCPName.MatchString(tool) || len(server.Tools) != 0 && !slices.Contains(server.Tools, tool) ||
			input.Cursor != "" || input.PageOffset != 0 || input.PageLimit != 0 || input.SchemaTool != "" {
			return "", mcptools.ArgumentError("$.tool", "该 MCP 工具未获当前连接授权")
		}
		target = server.ID + "__" + tool
		value, err := strictjson.Decode(input.Arguments)
		object, ok := value.(map[string]any)
		if err != nil || !ok {
			return "", mcptools.ArgumentError("$.arguments", "请提供工具参数对象")
		}
		args, err = json.Marshal(object)
		if err != nil {
			return "", err
		}
	} else if input.Tool != "" || len(input.Arguments) != 0 ||
		input.SchemaTool == "" && (input.TextOffset != 0 || input.TextLimit != 0) ||
		input.SchemaTool != "" && (input.PageLimit != 0 || input.PageOffset != 0) {
		return "", mcptools.ArgumentError("$", "读取 MCP 目录只接受连接和分页参数")
	}

	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	source, ok := t.store.SourceForInput(companion.Input{Channel: "local", Account: grant.FishID, Thread: grant.OwnerID, Sender: grant.OwnerID, TraceID: t.traceID})
	if !ok || source.EventID == "" {
		return "", errors.New("本轮 MCP 来源已变化")
	}
	digest := externalDigest(registration)
	configVersion, err := host.store.Configuration(ctx, localExternalPackage)
	if err != nil {
		return "", err
	}
	session, err := t.mcpSession(ctx, host, grant, server, digest, installed.Revision, configVersion.Revision)
	if err != nil {
		return "", err
	}
	cursor, known := session.cursors[input.Cursor]
	if input.Cursor != "" && !known {
		return "", mcptools.ArgumentError("$.cursor", "目录游标不属于本轮连接，请重新读取目录")
	}
	protocol, _ := json.Marshal(map[string]any{"version": 1, "server_id": server.ID, "allowlist": append([]string{}, server.Tools...)})
	request := localExternalMCPRequest{Operation: operation, ServerID: server.ID, Tool: tool, Arguments: args,
		ArgumentsSHA256: externalDigest(args), Protocol: protocol}
	// The durable request identity excludes registration changes. Changing an
	// endpoint or package cannot turn an uncertain outcome into a fresh call.
	key := localCheckpointKey("station-external-mcp", source.EventID, source.Revision, operation, server.ID, tool, args)
	if operation == "list" {
		key = localCheckpointKey(key, session.nonce, cursor)
	}
	request.RequestSHA256 = localCheckpointKey(key, digest, session.binding)
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	inv := pluginhost.Invocation{Account: grant.FishID, Thread: grant.OwnerID, Requester: grant.OwnerID,
		Purpose: "workstation MCP " + operation, RequestKey: key, Authorization: localCheckpointKey(grant.projectJobAuthorization(), digest, session.binding),
		SourceVersions: map[string]int{source.EventID: source.Revision}}
	binding := localExternalBinding{tools: t, host: host, installed: installed, configRev: configVersion.Revision, grant: grant, source: source,
		registration: digest, invocation: inv, capability: localExternalMCPCap, arguments: encoded, extraCheck: session.check}
	policy := &localExternalMCPPolicy{localExternalBinding: binding, session: session, server: server, request: request, cursor: cursor}
	view, output, err := binding.invoke(ctx, policy, name, target, operation == "call", input.TextOffset > 0 || input.PageOffset > 0 || input.SchemaTool != "")
	if err != nil {
		return "", err
	}
	if output == nil {
		return localProcessJSON(view)
	}
	view["untrusted_external_content"] = true
	if operation == "list" {
		return localExternalMCPPage(session, view, output, input)
	}
	if input.TextLimit == 0 {
		input.TextLimit = 1200
	}
	page, err := localExternalTextPage(output, input.TextOffset, input.TextLimit)
	if err != nil {
		return "", err
	}
	if page["is_error"] == true {
		view["state"] = "service_error"
	}
	view["output"] = page
	return localProcessJSON(view)
}

func localExternalMCPPage(session *stationMCPSession, view map[string]any, output json.RawMessage, input localExternalMCPInput) (string, error) {
	var page struct {
		Tools      []mcptools.Tool `json:"tools"`
		NextCursor string          `json:"next_cursor"`
	}
	if json.Unmarshal(output, &page) != nil || input.PageOffset > len(page.Tools) {
		return "", mcptools.ArgumentError("$.offset", "目录偏移超过已核对的工具数量")
	}
	if input.SchemaTool != "" {
		index := slices.IndexFunc(page.Tools, func(tool mcptools.Tool) bool { return tool.Name == input.SchemaTool })
		if index < 0 {
			return "", mcptools.ArgumentError("$.schema_tool", "本目录页没有这个工具")
		}
		if input.TextLimit == 0 {
			input.TextLimit = 1200
		}
		fields, _ := json.Marshal(map[string]any{"tool": input.SchemaTool, "text": string(page.Tools[index].InputSchema)})
		schema, err := localExternalTextPage(fields, input.TextOffset, input.TextLimit)
		if err != nil {
			return "", err
		}
		view["schema"] = schema
		return localProcessJSON(view)
	}
	if input.PageLimit == 0 {
		input.PageLimit = 8
	}
	end, size := input.PageOffset, 0
	for end < len(page.Tools) && end-input.PageOffset < input.PageLimit {
		data, _ := json.Marshal(page.Tools[end])
		if size+len(data) > 3800 && end > input.PageOffset {
			break
		}
		if len(data) > 3800 {
			// Keep a large declaration addressable without truncating its schema
			// into invalid JSON or losing the continuation to other tools.
			page.Tools[end].Description = "用 schema_tool 传这个工具名，在同一目录页读取完整参数声明。"
			page.Tools[end].InputSchema = nil
			size += 160
		} else {
			size += len(data)
		}
		end++
	}
	view["tools"], view["offset"], view["total_in_page"] = page.Tools[input.PageOffset:end], input.PageOffset, len(page.Tools)
	if end < len(page.Tools) {
		view["next_offset"] = end
	} else if page.NextCursor != "" {
		token := localCheckpointKey(session.nonce, page.NextCursor)
		session.cursors[token] = page.NextCursor
		view["next_cursor"] = token
	}
	return localProcessJSON(view)
}

type stationMCPSession struct {
	hub            *mcptools.Hub
	nonce, binding string
	registration   string
	cursors        map[string]string
	check          func(context.Context) error
}

func (t *localToolSet) mcpSession(ctx context.Context, host *localProjectToolHost, grant localWorkspaceGrant, server mcptools.ServerConfig, digest string, installedRevision, configRevision int) (*stationMCPSession, error) {
	if prior := t.mcpSessions[server.ID]; prior != nil {
		if prior.registration != digest || prior.check(ctx) != nil {
			_ = prior.hub.Close()
			return nil, errors.New("本轮 MCP 登记或授权已变化")
		}
		return prior, nil
	}
	if len(t.mcpSessions) >= 32 {
		return nil, errors.New("本轮 MCP 连接已达运行器容量，请在新请求中继续其他连接")
	}
	s := &stationMCPSession{registration: digest, nonce: rand.Text(), cursors: map[string]string{}}
	var installed pluginhost.InstalledPackage
	var revision int
	if server.Transport == "stdio" {
		var err error
		installed, revision, err = stationMCPPackage(ctx, host, server.PluginID)
		if err != nil {
			return nil, err
		}
		s.binding = localCheckpointKey(installed.Digest, installed.Revision, revision)
	}
	s.check = func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := t.projectTools.externalRegistration()
		if err != nil || externalDigest(raw) != digest || host.controller.Closed() || !t.access.current(grant) || t.run.CheckSource(ctx) != nil {
			return errors.New("MCP 当前连接或请求已撤权")
		}
		outer, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
		if err != nil || outer.Revision != installedRevision || !slices.Contains(outer.Permissions, "network") {
			return errors.New("外部工具连接已停用")
		}
		configuration, err := host.store.Configuration(ctx, localExternalPackage)
		if err != nil || configuration.Revision != configRevision {
			return errors.New("外部工具配置已变化")
		}
		if server.Transport == "stdio" {
			item, rev, err := stationMCPPackage(ctx, host, server.PluginID)
			if err != nil || localCheckpointKey(item.Digest, item.Revision, rev) != s.binding {
				return errors.New("已登记 MCP 工具包或配置已变化")
			}
		}
		return nil
	}
	launcher := func(ctx context.Context, c mcptools.ServerConfig) (mcptools.StdioProcess, error) {
		if c.PluginID != installed.Package.ID || c.ID != server.ID || c.Transport != "stdio" || s.check(ctx) != nil {
			return nil, errors.New("MCP 只能启动已核对的本机工具包")
		}
		options := t.projectTools.options
		options.DocumentRuntimeRoot = ""
		executor := stationruntime.InstalledToolExecutor(host.store, options, t.projectTools.home.root)
		resolve := executor.Resolve
		executor.Resolve = func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginruntime.LaunchSpec, error) {
			spec, err := resolve(ctx, p, g)
			if err != nil {
				return spec, err
			}
			spec.DataDir = filepath.Join(spec.DataDir, "mcp", localCheckpointKey(grant.FishID, grant.OwnerID, server.ID))
			root, err := workspacefs.EnsurePath(spec.DataDir)
			if err != nil {
				return spec, err
			}
			return spec, root.Close()
		}
		p := installed.Package
		stream, err := executor.StartStream(ctx, p, pluginhost.Grant{Account: grant.FishID, Plugin: p.ID, Version: p.Version, Permissions: installed.Permissions})
		if err != nil {
			return nil, err
		}
		if err := s.check(ctx); err != nil {
			_ = stream.Close()
			return nil, err
		}
		watched := &stationMCPStream{Stream: stream, stop: make(chan struct{})}
		go watched.watch(s.check)
		return watched, nil
	}
	hub, err := mcptools.New([]mcptools.ServerConfig{server}, mcptools.WithStdioLauncher(launcher), mcptools.WithServerGuard(func(ctx context.Context, _ mcptools.ServerConfig) error {
		return s.check(ctx)
	}))
	if err != nil {
		return nil, errors.New("工作站 MCP 登记无效")
	}
	s.hub = hub
	if t.mcpSessions == nil {
		t.mcpSessions = map[string]*stationMCPSession{}
	}
	t.mcpSessions[server.ID] = s
	return s, nil
}

func stationMCPPackage(ctx context.Context, host *localProjectToolHost, id string) (pluginhost.InstalledPackage, int, error) {
	i, err := host.store.Installed(ctx, id)
	if err != nil || i.Removed || !i.Enabled || i.Package.Runtime.Transport != "mcp-stdio" {
		return i, 0, errors.New("MCP 对应的本机工具包未安装或未启用")
	}
	config, err := host.store.Configuration(ctx, id)
	if err != nil {
		return i, 0, err
	}
	g := pluginhost.Grant{Account: host.store.Account(), Plugin: id, Version: i.Package.Version, Permissions: i.Permissions}
	inv := pluginhost.Invocation{Account: host.store.Account(), Thread: "host:mcp", Requester: "owner", Purpose: "registered MCP runtime", RequestKey: "station-mcp-runtime"}
	if err := pluginhost.Authorize(i.Package, g, inv, i.Package.Permissions); err != nil {
		return i, 0, err
	}
	return i, config.Revision, nil
}

type stationMCPStream struct {
	*pluginruntime.Stream
	stop chan struct{}
	once sync.Once
}

func (s *stationMCPStream) Close() error {
	s.once.Do(func() { close(s.stop) })
	return s.Stream.Close()
}

func (s *stationMCPStream) watch(check func(context.Context) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := check(ctx)
			cancel()
			if err != nil {
				_ = s.Close()
				return
			}
		}
	}
}

func (t *localToolSet) closeExternalMCP() {
	for _, session := range t.mcpSessions {
		_ = session.hub.Close()
	}
}

type localExternalMCPPolicy struct {
	localExternalBinding
	session *stationMCPSession
	server  mcptools.ServerConfig
	request localExternalMCPRequest
	cursor  string

	mu   sync.Mutex
	sent bool
	body json.RawMessage
	ref  plugins.ExternalHTTPBlobRef
}

func (p *localExternalMCPPolicy) Service(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sent || job.ID == "" || method != "wechat/external_mcp" || !strictjson.Matches(raw, map[string]string{
		"operation": p.request.Operation, "request_sha256": p.request.RequestSHA256,
	}) {
		return nil, errors.New("MCP 请求未获授权或已提交")
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return nil, err
	}
	p.sent = true
	bound, stop := p.watchedContext(ctx, job, cap)
	defer stop()
	ctx = mcptools.WithSessionScope(bound, p.session.nonce)
	method = "tools/list"
	params := map[string]any{}
	if p.cursor != "" {
		params["cursor"] = p.cursor
	}
	if p.request.Operation == "call" {
		method = "tools/call"
		params = map[string]any{"name": p.request.Tool, "arguments": p.request.Arguments}
	}
	payload, _ := json.Marshal(params)
	reply, err := p.session.hub.Request(ctx, p.server.ID, method, payload)
	if err != nil {
		if strings.Contains(err.Error(), "1 MiB") {
			return nil, errors.New("MCP 响应超过传输帧限制，请先核对原请求，再按服务支持的分页或素材引用处理")
		}
		return nil, errors.New("MCP 响应未确认，请核对原服务")
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return nil, err
	}
	p.body = reply
	p.ref = plugins.ExternalHTTPBlobRef{SHA256: externalDigest(reply), Size: int64(len(reply))}
	return plugins.ExternalMCPObservation(reply, p.ref)
}

func (p *localExternalMCPPolicy) Validate(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, job, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	var wire plugins.ExternalMCPWireResult
	if !p.sent || len(p.body) == 0 || strictjson.DecodeKnownFields(raw, &wire) != nil {
		return pluginruntime.VerifiedResult{}, errors.New("MCP 响应未通过核验")
	}
	output, err := plugins.VerifyExternalMCPResult(p.server, p.request.Operation, wire, p.body, p.ref)
	if err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if p.request.Operation == "list" {
		for i := range wire.Tools {
			wire.Tools[i].Description = maskMCPText(wire.Tools[i].Description, p.server.Headers)
		}
		data, _ := json.Marshal(map[string]any{"tools": wire.Tools, "next_cursor": wire.NextCursor})
		return pluginruntime.VerifiedResult{Result: json.RawMessage(`{"host_observed":true}`), Private: data}, nil
	}
	output.Text = maskMCPText(output.Text, p.server.Headers)
	if wire.IsError {
		output.Media = nil
	}
	return p.saveOutput(ctx, job, cap, p.server.ID+"__"+p.request.Tool, output, nil, map[string]any{"is_error": wire.IsError})
}

func maskMCPText(text string, headers map[string]string) string {
	for key, value := range headers {
		switch strings.ToLower(key) {
		case "authorization", "cookie", "x-api-key", "api-key":
			if value != "" {
				text = strings.ReplaceAll(text, value, "[已隐藏]")
				if _, token, ok := strings.Cut(value, " "); ok && !strings.Contains(token, " ") && token != "" {
					text = strings.ReplaceAll(text, token, "[已隐藏]")
				}
			}
		}
	}
	return text
}
