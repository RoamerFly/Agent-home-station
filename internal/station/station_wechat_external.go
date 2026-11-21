package station

import (
	"context"
	"encoding/json"
	"errors"

	"wechat-bot/internal/mcptools"
)

func (t *stationAttachmentTools) publicExternalCatalogTools(ctx context.Context) []mcptools.Tool {
	if t.projects == nil || t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return nil
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return nil
	}
	httpTools, taskTools, servers, err := t.projects.publicExternalCatalog(ctx, record.FishID)
	if err != nil {
		return nil
	}
	var tools []mcptools.Tool
	if len(httpTools) != 0 {
		tools = append(tools, mcptools.NewTool(localExternalHTTPList, "分页查看工作站已启用的同步 HTTP 工具及参数，不返回地址、请求头或密钥。", `{"type":"object","properties":{"query":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"default":8}},"additionalProperties":false}`))
		if t.externalMu != nil && t.store != nil {
			tools = append(tools, mcptools.NewTool(localExternalHTTPCall, "调用工作站已登记的同步 HTTP 工具，返回 JSON 文本或二进制文件。正文可分页；相同消息和参数只提交一次，结果不明时需核对原服务。", `{"type":"object","properties":{"tool":{"type":"string"},"arguments":{"type":"object"},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1,"default":1200}},"required":["tool","arguments"],"additionalProperties":false}`))
		}
	}
	if len(taskTools) != 0 {
		tools = append(tools, mcptools.NewTool(localExternalTaskList, "分页查看工作站已启用的异步 Job 和 Poll 工具及输入参数。", `{"type":"object","properties":{"query":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`))
	}
	if len(servers) != 0 {
		tools = append(tools, mcptools.NewTool(localExternalMCPServers, "列出工作站已启用的 MCP 连接名称、编号和传输类型，不联系服务。", `{"type":"object","properties":{},"additionalProperties":false}`))
	}
	return tools
}

func (t *stationAttachmentTools) callPublicExternalCatalog(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	available := false
	for _, tool := range t.publicExternalCatalogTools(ctx) {
		available = available || tool.Name == name
	}
	if !available {
		return "", errors.New("工作站外部工具目录当前不可用")
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	httpTools, taskTools, servers, err := t.projects.publicExternalCatalog(ctx, record.FishID)
	if err != nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return "", errors.New("工作站外部工具目录当前不可用")
	}
	switch name {
	case localExternalHTTPList:
		return listLocalExternalHTTP(raw, httpTools)
	case localExternalTaskList:
		return listLocalExternalHTTP(raw, taskTools)
	case localExternalMCPServers:
		if strictLocalMemoryDecode(raw, &struct{}{}) != nil {
			return "", mcptools.ArgumentError("$", "MCP 连接列表无需参数")
		}
		views := make([]map[string]string, 0, len(servers))
		for _, server := range servers {
			views = append(views, map[string]string{"server_id": server.ID, "name": server.Name, "transport": server.Transport})
		}
		return localProcessJSON(map[string]any{"servers": views})
	}
	return "", errors.New("未知外部工具目录")
}
