package adapter

import (
	"encoding/json"

	"wechat-bot/internal/mcptools"
)

// Policy is the host-resolved authorization snapshot for WeChat tool discovery.
// The host remains responsible for account, preview, recipient, group-observe,
// and current-conversation scope gates before constructing this value.
type Policy struct {
	Owner             bool
	ReadConversations bool
	CrossConversation bool
	AccountActions    []string
}

func tool(name, description, schema string) mcptools.Tool {
	return mcptools.Tool{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}

// Tools returns WeChat MCP tools visible under the supplied host policy.
func Tools(policy Policy) []mcptools.Tool {
	tools := make([]mcptools.Tool, 0, 11)
	if policy.ReadConversations {
		tools = append(tools, tool("wechat_read", "读取消息或群成员，默认当前会话。主人跨会话读取可填 wechat_conversations 返回的 thread_id。首次省略 cursor，后续使用 next_cursor；消息保留原编号、发送者和时间。", `{"type":"object","properties":{"kind":{"type":"string","enum":["messages","members"]},"cursor":{"type":"string","maxLength":8192},"thread_id":{"type":"string"}},"required":["kind"],"additionalProperties":false}`))
	}
	tools = append(tools,
		tool("wechat_file_read", "按原消息编号取得当前会话收到或已发送的文件，返回 asset_id 供后续读取、修改或转换；主人可指定获准的 thread_id。", `{"type":"object","properties":{"message_id":{"type":"string"},"thread_id":{"type":"string"}},"required":["message_id"],"additionalProperties":false}`),
		tool("wechat_delivery_status", "按 job_id 查询原生投递回执及分段状态；主人可指定获准的 thread_id。accepted 表示微信服务端已接收。", `{"type":"object","properties":{"job_id":{"type":"string"},"thread_id":{"type":"string"}},"required":["job_id"],"additionalProperties":false}`),
	)
	if !policy.Owner {
		return tools
	}
	if contains(policy.AccountActions, "recall") {
		tools = append(tools, tool("wechat_recall", "撤回本账号已发消息。填真实 message_id、稳定 request_key，可指定获准的 thread_id；返回微信实际结果。", `{"type":"object","properties":{"message_id":{"type":"string"},"request_key":{"type":"string"},"thread_id":{"type":"string"}},"required":["message_id","request_key"],"additionalProperties":false}`))
	}
	tools = append(tools,
		tool("wechat_conversations", "列出本账号已知会话及编号，含未开启自动回复的群。query 筛选，offset/limit 分页，refresh 更新目录；返回 next_offset 和来源覆盖。", `{"type":"object","properties":{"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1},"refresh":{"type":"boolean"},"query":{"type":"string"}},"additionalProperties":false}`),
		tool("wechat_operation_status", "按 operation_id 查询原操作及原生回执。", `{"type":"object","properties":{"operation_id":{"type":"string"}},"required":["operation_id"],"additionalProperties":false}`),
	)
	if policy.CrossConversation {
		tools = append(tools,
			tool("wechat_send_media", "向指定会话发送已缓存媒体或转发原消息。视频默认用 kind=video，作为微信聊天里可直接播放的原生视频；只有明确要求文件附件或原始文件时用 kind=file。转发刚发的图片或视频时，先用 wechat_read 取得那条媒体消息的 message_id；不要填用户的文字请求编号。填来源会话、目标会话和稳定 request_key。已有准确素材可填 asset_id。返回 operation_id 用 wechat_operation_status 核对；只有拿到 job_id 才用 wechat_delivery_status。", WithVoiceConversion(`{"type":"object","properties":{"source_thread_id":{"type":"string"},"asset_id":{"type":"string"},"message_id":{"type":"string"},"target_thread_id":{"type":"string"},"kind":{"type":"string","enum":["file","voice","image","video"]},"request_key":{"type":"string"}},"required":["kind","request_key"],"additionalProperties":false}`)),
			tool("wechat_forward_file", "按原消息编号把另一获准会话的文件原件转发到目标会话。用 wechat_conversations 和 wechat_read 找来源编号；填稳定 request_key，之后用 wechat_operation_status 查回执。", `{"type":"object","properties":{"source_thread_id":{"type":"string"},"message_id":{"type":"string"},"target_thread_id":{"type":"string"},"request_key":{"type":"string"}},"required":["source_thread_id","message_id","request_key"],"additionalProperties":false}`),
			tool("wechat_send_text", "向当前或其他获准会话发送文字。每次逻辑发送使用稳定 request_key；用 wechat_operation_status 查回执，最终回复不重复发送。", `{"type":"object","properties":{"thread_id":{"type":"string"},"text":{"type":"string"},"request_key":{"type":"string"}},"required":["thread_id","text","request_key"],"additionalProperties":false}`),
		)
	}
	tools = append(tools, tool("wechat_delivery_retry", "续办原媒体投递 job_id，只发送尚未确认的分段并保留已接收回执。本次尝试用独立 request_key；主人跨会话续办可填获准 thread_id。", `{"type":"object","properties":{"job_id":{"type":"string"},"request_key":{"type":"string"},"thread_id":{"type":"string"}},"required":["job_id","request_key"],"additionalProperties":false}`))
	if len(policy.AccountActions) > 0 {
		actions, _ := json.Marshal(policy.AccountActions)
		tools = append(tools, tool("wechat_account_action", "执行本轮获准的账号操作："+string(actions)+"。填实际会话与对象编号，依据回执判断结果。", `{"type":"object","properties":{"kind":{"type":"string"},"thread_id":{"type":"string"},"text":{"type":"string"},"members":{"type":"array","items":{"type":"string"}},"muted":{"type":"boolean"},"message_id":{"type":"string"},"server_id":{"type":"string"}},"required":["kind","thread_id"],"additionalProperties":false}`))
	}
	return tools
}

// ToolName reports whether name is one of this adapter's exact tool names.
func ToolName(name string) bool {
	switch name {
	case "wechat_read", "wechat_file_read", "wechat_delivery_status", "wechat_recall", "wechat_conversations", "wechat_operation_status", "wechat_send_media", "wechat_forward_file", "wechat_send_text", "wechat_delivery_retry", "wechat_account_action":
		return true
	default:
		return false
	}
}

// SubmissionTool reports tools which require the host's pre-submission gate.
func SubmissionTool(name string) bool {
	switch name {
	case "wechat_send_text", "wechat_send_media", "wechat_forward_file", "wechat_account_action", "wechat_recall", "wechat_delivery_retry":
		return true
	default:
		return false
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
