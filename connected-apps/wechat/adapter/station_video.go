package adapter

import "wechat-bot/internal/mcptools"

func StationVideoTool() mcptools.Tool {
	return tool("wechat_video_read", "按 message_id 取得并探测本会话的视频或视频号原件；返回 asset_id 后用 wechat_video_observe 读画面、音轨。max_bytes 和 timeout_seconds 可调整。", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192},"max_bytes":{"type":"integer","minimum":1,"maximum":536870912},"timeout_seconds":{"type":"integer","minimum":0}},"required":["message_id"],"additionalProperties":false}`)
}

func StationVideoObservationTool() mcptools.Tool {
	return tool("wechat_video_observe", "按 wechat_video_read 返回的 message_id、asset_id 读取视频。mode=frames 可逐帧或按 step 采样，按 next_start_frame 续读；mode=audio 转写音轨，按 next_start_seconds 续读。以返回的实际覆盖范围描述内容。", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192},"asset_id":{"type":"string","maxLength":128},"mode":{"type":"string","enum":["frames","audio"]},"start_frame":{"type":"integer","minimum":0,"default":0},"step":{"type":"integer","minimum":1,"default":1},"page_size":{"type":"integer","minimum":1,"default":12},"batch_size":{"type":"integer","minimum":1,"default":6},"max_dimension":{"type":"integer","minimum":1,"default":960},"analysis_tokens":{"type":"integer","minimum":1,"default":1024},"question":{"type":"string","maxLength":4096},"start_seconds":{"type":"number","minimum":0,"default":0},"duration_seconds":{"type":"number","exclusiveMinimum":0,"default":90},"timeout_seconds":{"type":"integer","minimum":0,"default":240}},"required":["message_id","asset_id","mode"],"additionalProperties":false}`)
}
