package station

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
)

// Catalog requests use the workstation's one installed registration. Secret
// values remain in the host; the application receives public tool metadata.
func (s *localProjectTools) publicExternalCatalog(ctx context.Context, fish string) ([]plugins.ExternalProtocolTool, []plugins.ExternalProtocolTool, []mcptools.ServerConfig, error) {
	if s == nil {
		return nil, nil, nil, errors.New("工作站外部工具不可用")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return nil, nil, nil, err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return nil, nil, nil, errors.New("工作站外部工具未启用或缺少联网授权")
	}
	raw, err := s.externalRegistration()
	if err != nil {
		return nil, nil, nil, errors.New("工作站外部工具登记不可读")
	}
	config, err := parseLocalExternalRegistration(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	var httpTools, taskTools []plugins.ExternalProtocolTool
	if len(config.Connections) != 0 {
		if _, bridge, bridgeErr := externalBridgeRegistration(raw); bridgeErr == nil {
			httpTools, taskTools = bridge.Protocol().Tools, bridge.TaskTools()
		}
	}
	servers, serverErr := registeredStationMCPServers(config)
	if serverErr != nil {
		servers = nil
	}
	if len(httpTools) == 0 && len(taskTools) == 0 && len(servers) == 0 {
		return nil, nil, nil, errors.New("工作站外部工具没有可用目录")
	}
	return httpTools, taskTools, servers, nil
}

// The backend performs one exact request using the current host registration.
// The application supplies only an opaque registration digest and public tool
// arguments; no endpoint or credential enters the application result.
func (s *localProjectTools) publicExternalHTTP(ctx context.Context, fish, registration, name string, arguments json.RawMessage) (plugins.Output, error) {
	host, err := s.forFish(fish)
	if err != nil {
		return plugins.Output{}, err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return plugins.Output{}, errors.New("工作站外部工具未启用或缺少联网授权")
	}
	configuration, err := host.store.Configuration(ctx, localExternalPackage)
	if err != nil {
		return plugins.Output{}, err
	}
	raw, err := s.externalRegistration()
	if err != nil || externalDigest(raw) != registration {
		return plugins.Output{}, errors.New("工作站外部工具登记已变化")
	}
	connections, bridge, err := externalBridgeRegistration(raw)
	if err != nil {
		return plugins.Output{}, err
	}
	protocol := bridge.Protocol()
	index := slices.IndexFunc(protocol.Tools, func(tool plugins.ExternalProtocolTool) bool { return tool.Name == name })
	if index < 0 {
		return plugins.Output{}, errors.New("工作站外部工具未启用")
	}
	tool := protocol.Tools[index]
	if tool.MediaURLPath != "" || tool.TextPath == "" && tool.MediaKind == "" {
		return plugins.Output{}, errors.New("该工具没有当前微信适配器支持的响应类型")
	}
	reply, err := bridge.Execute(ctx, plugins.ExternalRequest{Phase: "call", Tool: name, Arguments: arguments})
	if err != nil {
		return plugins.Output{}, errors.New("外部请求结果未确认，请核对原服务")
	}
	current, checkErr := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	configNow, configErr := host.store.Configuration(ctx, localExternalPackage)
	rawNow, rawErr := s.externalRegistration()
	if checkErr != nil || configErr != nil || rawErr != nil || current.Revision != installed.Revision ||
		configNow.Revision != configuration.Revision || externalDigest(rawNow) != registration || ctx.Err() != nil {
		return plugins.Output{}, errors.New("外部请求期间授权或登记已变化，结果需要人工核对")
	}
	body, err := base64.StdEncoding.Strict().DecodeString(reply.BodyBase64)
	if err != nil {
		return plugins.Output{}, errors.New("外部响应编码无效")
	}
	if !plugins.ExternalHTTPIsJSON(body, reply.ContentType) {
		if tool.MediaKind == "" || len(body) == 0 {
			return plugins.Output{}, errors.New("外部响应没有声明可用的文件结果")
		}
		proof := plugins.Output{Media: []plugins.MediaResult{{Kind: tool.MediaKind, Base64Data: reply.BodyBase64, MIMEType: reply.ContentType}}}
		if !plugins.VerifyExternalHTTPOutput(tool, reply, proof) {
			return plugins.Output{}, errors.New("外部文件未通过内容核验")
		}
		return plugins.Output{Media: []plugins.MediaResult{{Kind: tool.MediaKind, Data: body, MIMEType: reply.ContentType}}}, nil
	}
	if len(body) > 2<<20 || tool.TextPath == "" {
		return plugins.Output{}, errors.New("外部响应没有受支持的 JSON 文本结果")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return plugins.Output{}, errors.New("外部响应 JSON 无效")
	}
	text := plugins.ExternalHTTPText(plugins.ExternalHTTPValue(value, tool.TextPath))[0]
	output := plugins.Output{Text: text}
	if !plugins.VerifyExternalHTTPOutput(tool, reply, output) {
		return plugins.Output{}, errors.New("外部响应未通过内容核验")
	}
	for _, secret := range externalConnectionSecrets(connections, name) {
		text = strings.ReplaceAll(text, secret, "[已隐藏]")
	}
	return plugins.Output{Text: text}, nil
}
