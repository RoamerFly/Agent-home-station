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
