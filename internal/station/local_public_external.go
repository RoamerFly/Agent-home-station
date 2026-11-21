package station

import (
	"context"
	"errors"
	"slices"

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
