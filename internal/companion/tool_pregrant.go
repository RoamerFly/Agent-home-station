package companion

import (
	"encoding/json"
	"strings"
)

// A missing observation on a non-file incoming attachment already points to
// the host's scoped reader. Exposing its granted schema avoids a discovery
// round; the host still binds every actual read to the original message.
func needsIncomingMediaRead(turns []map[string]string) bool {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i]["role"] != "user" {
			continue
		}
		var packet struct {
			Kind          string `json:"kind"`
			MediaReadHint string `json:"media_read_hint"`
			Observations  []struct {
				Available *bool `json:"result_available"`
				Source    struct {
					MessageType int `json:"message_type"`
				} `json:"source"`
			} `json:"media_observations"`
		}
		if json.Unmarshal([]byte(turns[i]["content"]), &packet) != nil || packet.Kind != "current_message" {
			continue
		}
		if !strings.Contains(packet.MediaReadHint, "incoming_media_read") {
			return false
		}
		for _, observation := range packet.Observations {
			if observation.Available != nil && !*observation.Available && supportedIncomingReadType(observation.Source.MessageType) {
				return true
			}
		}
		return false
	}
	return false
}

func supportedIncomingReadType(messageType int) bool {
	switch messageType {
	case 3, 47, 43, 49: // image, sticker, video and share
		return true
	}
	return false
}

func needsMediaForward(turns []map[string]string) bool {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i]["role"] != "user" {
			continue
		}
		var packet struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(turns[i]["content"]), &packet) != nil {
			return false
		}
		if packet.Kind != "current_message" {
			continue
		}
		return (strings.Contains(packet.Text, "转发") || strings.Contains(strings.ToLower(packet.Text), "forward")) &&
			(strings.Contains(packet.Text, "视频") || strings.Contains(packet.Text, "图片") || strings.Contains(packet.Text, "文件") || strings.Contains(packet.Text, "语音") || strings.Contains(packet.Text, "这个") || strings.Contains(packet.Text, "这条"))
	}
	return false
}

func needsComfyVideo(turns []map[string]string) bool {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i]["role"] != "user" {
			continue
		}
		var packet struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(turns[i]["content"]), &packet) != nil || packet.Kind != "current_message" {
			continue
		}
		text := strings.ToLower(packet.Text)
		return strings.Contains(text, "生成视频") || strings.Contains(text, "生视频") || strings.Contains(text, "做视频") ||
			strings.Contains(text, "制作视频") || strings.Contains(text, "做成视频")
	}
	return false
}
