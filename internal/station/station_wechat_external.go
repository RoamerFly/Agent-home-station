package station

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
			tools = append(tools, mcptools.NewTool(localExternalHTTPCall, "调用工作站已登记的同步 HTTP 工具，返回正文或文件。媒体待领取时用相同参数查询原任务；相同消息和参数只提交一次，回执不明时需核对原服务。", `{"type":"object","properties":{"tool":{"type":"string"},"arguments":{"type":"object"},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1,"default":1200}},"required":["tool","arguments"],"additionalProperties":false}`))
		}
	}
	if len(taskTools) != 0 {
		tools = append(tools, mcptools.NewTool(localExternalTaskList, "分页查看工作站已启用的异步 Job 和 Poll 工具及输入参数。", `{"type":"object","properties":{"query":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`))
		if t.externalMu != nil && t.store != nil {
			tools = append(tools, mcptools.NewTool(localExternalTaskCall, "提交、查询或取消工作站已登记的异步任务。媒体待领取时用 status 读取原任务。每个请求阶段只提交一次；正文可分页，回执不明时请核对原服务。", `{"type":"object","properties":{"action":{"type":"string","enum":["start","status","cancel"]},"tool":{"type":"string"},"arguments":{"type":"object"},"task_id":{"type":"string"},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1}},"required":["action"],"additionalProperties":false}`))
		}
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

func publicExternalMediaPending(base string, fetchErr error) (string, error) {
	var view map[string]any
	if json.Unmarshal([]byte(base), &view) != nil {
		return "", errors.New("外部媒体任务状态不可读")
	}
	code := "external_media_fetch_unconfirmed"
	if fetchErr != nil && strings.HasPrefix(fetchErr.Error(), "external_media_") {
		code = fetchErr.Error()
	}
	view["media_error"] = code
	if code == "external_media_origin_unapproved" {
		view["instruction"] = "返回的媒体来源未获工作站登记许可；请人工核对原任务和来源，原请求不会重发。"
	}
	return localProcessJSON(view)
}
