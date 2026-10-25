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
		var in struct{ Kind, Text string }
		if turns[i]["role"] == "user" && json.Unmarshal([]byte(turns[i]["content"]), &in) == nil && in.Kind == "current_message" {
			return in.Text
		}
	}
	return ""
}
