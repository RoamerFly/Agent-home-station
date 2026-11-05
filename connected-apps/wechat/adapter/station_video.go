package adapter

import "wechat-bot/internal/mcptools"

func StationVideoTool() mcptools.Tool {
	return tool("wechat_video_read", "Acquire the actual video original of a received video or WeChat Channels card in this conversation, and probe its duration/frame count/dimensions. Current or verified quoted media needs media-read authorization; older messages also need history authorization. This tool returns a private asset ID and verified file metadata, NOT visual understanding or audio transcription. Never summarize card copy as watched content. Missing resolver or decoder is reported as a distinct stage. max_bytes defaults52428800; transport ceiling536870912, no silent clamp. timeout_seconds defaults240;0 uses caller cancellation. No message sends. Input {message_id,max_bytes?,timeout_seconds?}.", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192},"max_bytes":{"type":"integer","minimum":1,"maximum":536870912},"timeout_seconds":{"type":"integer","minimum":0}},"required":["message_id"],"additionalProperties":false}`)
}
