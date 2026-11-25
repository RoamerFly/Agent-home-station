package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/stationruntime"
	"wechat-bot/internal/workspacefs"
)

// The application supplies an approved server, operation, and opaque
// registration digest. Account authorization and submit-once reservations stay
// with that application; this backend never receives its message source.
type publicExternalMCPResult struct {
	Tools      []mcptools.Tool
	NextCursor string
	Output     plugins.Output
	IsError    bool
}

type publicExternalMCPSession struct {
	hub     *mcptools.Hub
	binding string
}

func (s *localProjectTools) publicExternalMCP(ctx context.Context, fish, registration, serverID, operation, tool, cursor string, arguments json.RawMessage) (publicExternalMCPResult, error) {
	var empty publicExternalMCPResult
	host, err := s.forFish(fish)
	if err != nil {
		return empty, err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return empty, errors.New("工作站外部工具未启用或缺少联网授权")
	}
	configuration, err := host.store.Configuration(ctx, localExternalPackage)
	if err != nil {
		return empty, err
	}
	raw, err := s.externalRegistration()
	if err != nil || externalDigest(raw) != registration {
		return empty, errors.New("工作站 MCP 登记已变化")
	}
	config, err := parseLocalExternalRegistration(raw)
	if err != nil {
		return empty, err
	}
	servers, err := registeredStationMCPServers(config)
	if err != nil {
		return empty, err
	}
	index := slices.IndexFunc(servers, func(server mcptools.ServerConfig) bool { return server.ID == serverID })
	if index < 0 {
		return empty, errors.New("工作站 MCP 连接未启用")
	}
	server := servers[index]
	if operation != "list" && operation != "call" || operation == "list" && (tool != "" || len(arguments) != 0) ||
		operation == "call" && (cursor != "" || !plugins.ExternalMCPName.MatchString(tool) || len(server.Tools) != 0 && !slices.Contains(server.Tools, tool)) {
		return empty, errors.New("工作站 MCP 请求无效或工具未获授权")
	}
	var packageDigest string
	var runtime pluginhost.InstalledPackage
	var runtimeConfigRevision int
	if server.Transport == "stdio" {
		runtime, runtimeConfigRevision, err = stationMCPPackage(ctx, host, server.PluginID)
		if err != nil {
			return empty, err
		}
		packageDigest = runtime.Digest
	}
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
		if err != nil || current.Revision != installed.Revision || !slices.Contains(current.Permissions, "network") || host.controller.Closed() {
			return errors.New("工作站 MCP 授权已变化")
		}
		currentConfig, err := host.store.Configuration(ctx, localExternalPackage)
		currentRaw, rawErr := s.externalRegistration()
		if err != nil || rawErr != nil || currentConfig.Revision != configuration.Revision || externalDigest(currentRaw) != registration {
			return errors.New("工作站 MCP 登记已变化")
		}
		if server.Transport == "stdio" {
			currentRuntime, currentRevision, err := stationMCPPackage(ctx, host, server.PluginID)
			if err != nil || currentRuntime.Digest != packageDigest || currentRuntime.Revision != runtime.Revision || currentRevision != runtimeConfigRevision {
				return errors.New("工作站 MCP 工具包已变化")
			}
		}
		return nil
	}
	launcher := func(ctx context.Context, declaration mcptools.ServerConfig) (mcptools.StdioProcess, error) {
		if server.Transport != "stdio" || declaration.ID != server.ID || declaration.PluginID != runtime.Package.ID || check(ctx) != nil {
			return nil, errors.New("MCP 只能启动已核对的本机工具包")
		}
		options := s.options
		options.DocumentRuntimeRoot = ""
		executor := stationruntime.InstalledToolExecutor(host.store, options, s.home.root)
		resolve := executor.Resolve
		executor.Resolve = func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginruntime.LaunchSpec, error) {
			spec, err := resolve(ctx, p, g)
			if err != nil {
				return spec, err
			}
			spec.DataDir = filepath.Join(spec.DataDir, "mcp", localCheckpointKey(fish, server.ID))
			root, err := workspacefs.EnsurePath(spec.DataDir)
			if err != nil {
				return spec, err
			}
			return spec, root.Close()
		}
		p := runtime.Package
		stream, err := executor.StartStream(ctx, p, pluginhost.Grant{Account: fish, Plugin: p.ID, Version: p.Version, Permissions: runtime.Permissions})
		if err != nil {
			return nil, err
		}
		if err := check(ctx); err != nil {
			_ = stream.Close()
			return nil, err
		}
		watched := &stationMCPStream{Stream: stream, stop: make(chan struct{})}
		go watched.watch(check)
		return watched, nil
	}
	// MCP cursors and HTTP session IDs belong to a live server session. Keep
	// that session for subsequent pages, while serializing use and replacing it
	// whenever any installed declaration changes.
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	key := localCheckpointKey(fish, serverID)
	binding := localCheckpointKey(registration, installed.Revision, configuration.Revision, packageDigest, runtime.Revision, runtimeConfigRevision)
	session, ok := s.mcpSessions[key]
	if ok && session.binding != binding {
		_ = session.hub.Close()
		delete(s.mcpSessions, key)
		ok = false
	}
	if !ok {
		hub, err := mcptools.New([]mcptools.ServerConfig{server}, mcptools.WithStdioLauncher(launcher), mcptools.WithServerGuard(func(ctx context.Context, _ mcptools.ServerConfig) error { return check(ctx) }))
		if err != nil {
			return empty, errors.New("工作站 MCP 登记无效")
		}
		if s.mcpSessions == nil {
			s.mcpSessions = map[string]publicExternalMCPSession{}
		}
		session = publicExternalMCPSession{hub: hub, binding: binding}
		s.mcpSessions[key] = session
	}
	method := "tools/list"
	params := map[string]any{}
	if cursor != "" {
		params["cursor"] = cursor
	}
	if operation == "call" {
		method = "tools/call"
		params = map[string]any{"name": tool, "arguments": arguments}
	}
	payload, _ := json.Marshal(params)
	if err := check(ctx); err != nil {
		return empty, err
	}
	reply, err := session.hub.Request(ctx, serverID, method, payload)
	if err != nil {
		if strings.Contains(err.Error(), "1 MiB") {
			return empty, errors.New("MCP 响应超过 1 MiB，请核对原请求后使用服务分页或素材引用")
		}
		return empty, errors.New("MCP 响应未确认，请核对原服务")
	}
	if err := check(ctx); err != nil {
		return empty, errors.New("MCP 请求期间授权已变化，结果需要人工核对")
	}
	result, err := verifiedPublicExternalMCP(server, operation, reply)
	if err != nil {
		return empty, err
	}
	for i := range result.Tools {
		result.Tools[i].Description = maskMCPText(result.Tools[i].Description, server.Headers)
	}
	result.Output.Text = maskMCPText(result.Output.Text, server.Headers)
	if result.IsError {
		result.Output.Media = nil
	}
	return result, nil
}

func verifiedPublicExternalMCP(server mcptools.ServerConfig, operation string, reply json.RawMessage) (publicExternalMCPResult, error) {
	var result publicExternalMCPResult
	wire := plugins.ExternalMCPWireResult{Observation: plugins.ExternalHTTPBlobRef{SHA256: externalDigest(reply), Size: int64(len(reply))}}
	if operation == "list" {
		var page struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(reply, &page) != nil {
			return result, errors.New("MCP 工具目录无效")
		}
		for _, item := range page.Tools {
			if !plugins.ExternalMCPName.MatchString(item.Name) || len(server.Tools) != 0 && !slices.Contains(server.Tools, item.Name) {
				continue
			}
			schema := item.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			wire.Tools = append(wire.Tools, mcptools.Tool{Name: server.ID + "__" + item.Name, Description: item.Description, InputSchema: schema})
		}
		wire.NextCursor = page.NextCursor
	} else {
		var call struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Data     string `json:"data"`
				MIMEType string `json:"mimeType"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(reply, &call) != nil {
			return result, errors.New("MCP 工具结果无效")
		}
		content := &struct {
			Text  string                     `json:"text"`
			Media []plugins.ExternalMCPMedia `json:"media,omitempty"`
		}{}
		texts := []string{}
		for index, item := range call.Content {
			switch item.Type {
			case "text":
				texts = append(texts, item.Text)
			case "image", "audio":
				data, err := base64.StdEncoding.Strict().DecodeString(item.Data)
				if err != nil {
					return result, errors.New("MCP 媒体编码无效")
				}
				kind := "image"
				if item.Type == "audio" {
					kind = "voice"
				}
				content.Media = append(content.Media, plugins.ExternalMCPMedia{Kind: kind, ContentIndex: index, HostMediaRef: externalDigest(data), MIMEType: item.MIMEType})
			default:
				return result, errors.New("MCP 内容类型需使用素材引用")
			}
		}
		content.Text = strings.Join(texts, "\n")
		wire.Output, wire.IsError = content, call.IsError
	}
	output, err := plugins.VerifyExternalMCPResult(server, operation, wire, reply, wire.Observation)
	if err != nil {
		return result, err
	}
	result.Tools, result.NextCursor, result.Output, result.IsError = wire.Tools, wire.NextCursor, output, wire.IsError
	return result, nil
}
