package companion

import "encoding/json"

// Only a short, structured recent context is used by the independent classifier.
// It must understand confirmations without receiving every tool description.
func songIntentRequest(turns []map[string]string) string {
	recent := []json.RawMessage{}
	for _, turn := range turns {
		var message struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(turn["content"]), &message) != nil || (message.Kind != "history_message" && message.Kind != "history_reply") || len(turn["content"]) > 2400 {
			continue
		}
		recent = append(recent, json.RawMessage(turn["content"]))
		if len(recent) > 4 {
			recent = recent[1:]
		}
	}
	b, _ := json.Marshal(map[string]any{"current_message": currentMessage(turns), "recent_context": recent})
	return string(b)
}
