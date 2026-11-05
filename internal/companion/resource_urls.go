package companion

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

var publicURLs = regexp.MustCompile(`https?://[^\s<>"'\\]+`)

func rememberResourceURLs(set map[string]bool, text string) {
	var value any
	if json.Unmarshal([]byte(text), &value) == nil {
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case string:
				collectURLs(set, x)
			case []any:
				for _, i := range x {
					walk(i)
				}
			case map[string]any:
				for _, i := range x {
					walk(i)
				}
			}
		}
		walk(value)
		return
	}
	collectURLs(set, text)
}
func collectURLs(set map[string]bool, text string) {
	for _, raw := range publicURLs.FindAllString(text, 300) {
		raw = strings.TrimRight(raw, `.,;)]}，。；`)
		if normalized := resourceURL(raw); normalized != "" {
			set[normalized] = true
		}
	}
}
func resourceURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	return u.String()
}
func currentMessage(turns []map[string]string) string {
	for i := len(turns) - 1; i >= 0; i-- {
		var in struct {
			Kind, Text      string
			Account, Thread string
			MessageID       string `json:"message_id"`
			TaskID          string `json:"task_id"`
			Task            bool   `json:"task_continuation"`
			Background      bool   `json:"background_result"`
			Surf            bool   `json:"surf"`
			Runtime         struct {
				Kind, Producer, Text, Account, Thread string
				MessageID                             string `json:"request_message_id"`
				TaskID                                string `json:"task_id"`
			} `json:"runtime_context"`
			Events []struct{ Text string }
		}
		if turns[i]["role"] == "user" && json.Unmarshal([]byte(turns[i]["content"]), &in) == nil {
			if in.Kind == "current_followup" && len(in.Events) > 0 {
				return in.Events[len(in.Events)-1].Text
			}
			if in.Kind == "current_message" {
				if in.Runtime.Producer == "runtime" && (in.Runtime.Kind == "task_goal" || in.Runtime.Kind == "unread_digest") && in.Runtime.Account == in.Account && in.Runtime.Thread == in.Thread && in.Runtime.MessageID == in.MessageID && in.Runtime.TaskID == in.TaskID && (in.Task || in.Background || in.Surf || in.Runtime.Kind == "unread_digest") {
					return in.Runtime.Text
				}
				return in.Text
			}
		}
	}
	return ""
}

func currentMetadataOnly(turns []map[string]string) bool {
	for i := len(turns) - 1; i >= 0; i-- {
		var packet struct {
			Kind, Text   string
			Observations []struct {
				Kind, Observation string
				State             string            `json:"observation_state"`
				Parts             []ObservationPart `json:"observation_parts"`
			} `json:"media_observations"`
		}
		if turns[i]["role"] != "user" || json.Unmarshal([]byte(turns[i]["content"]), &packet) != nil || packet.Kind != "current_message" {
			continue
		}
		for _, observation := range packet.Observations {
			if len(observation.Parts) > 0 {
				if HasFirsthandObservation(observation.Parts) {
					// Typed observations are authoritative; do not require the
					// historical display envelope to recognise an actual read.
					continue
				}
				if observation.State == "observed" {
					return true
				}
			}
			// A share is a transport type. Successful share readers prepend this
			// envelope before publisher text, optionally after the asset receipt.
			body := observation.Observation
			if strings.HasPrefix(body, "[已缓存本会话原视频素材：") {
				_, body, _ = strings.Cut(body, "\n")
			}
			if observation.State == "observed" && strings.HasPrefix(body, ObservedShareHeader+"\n") {
				continue
			}
			if observation.Kind == "share" || observation.Kind == "card" || observation.State == "metadata_only" || strings.Contains(observation.Observation, "[收到分享卡片，仅依据卡片标题与简介，尚未观看视频") {
				return true
			}
		}
		return false
	}
	return false
}
