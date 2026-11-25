package adapter

import "wechat-bot/internal/mcptools"

// These station tools read only the current host-authorized conversation.
func StationCatalogTool() mcptools.Tool {
	return tool("wechat_read", "读取当前会话的消息或群成员；search/limit 调整范围，按 next_cursor 续读。消息保留发送者、时间和编号，完整内容用 wechat_message_read。", `{"type":"object","properties":{"kind":{"type":"string","enum":["messages","members"]},"search":{"type":"string","maxLength":256},"cursor":{"type":"string","maxLength":8192},"limit":{"type":"integer","minimum":1}},"required":["kind"],"additionalProperties":false}`)
}

func StationMessageTool() mcptools.Tool {
	return tool("wechat_message_read", "按 message_id 读取当前会话的原消息全文或分享卡片；用 offset/length 和 expected_sha256 续读。附件原件用 wechat_file_read。", `{"type":"object","properties":{"message_id":{"type":"string"},"offset":{"type":"integer","minimum":0},"length":{"type":"integer","minimum":1},"expected_sha256":{"type":"string"}},"required":["message_id"],"additionalProperties":false}`)
}

func StationLegacyMemoryTool() mcptools.Tool {
	return tool("wechat_read_legacy", "读取当前会话的旧档案。events 为原消息，memories 为带来源的派生记忆；max_bytes 控制文本量，按 next_cursor 续读。", `{"type":"object","properties":{"kind":{"type":"string","enum":["events","memories"]},"max_bytes":{"type":"integer","minimum":256,"maximum":65536},"offset":{"type":"integer","minimum":0},"cursor":{"type":"string","maxLength":8192},"limit":{"type":"integer","minimum":1,"maximum":100}},"required":["kind","max_bytes"],"additionalProperties":false}`)
}
