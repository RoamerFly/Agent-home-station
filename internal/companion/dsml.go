package companion

import (
	"encoding/json"
	"regexp"
	"strings"
)

var dsmlInvoke = regexp.MustCompile(`(?s)<dsml invoke name="([A-Za-z][A-Za-z0-9_-]*)">(.*?)</dsml invoke>`)
var dsmlArguments = regexp.MustCompile(`(?s)<dsml parameter name="arguments"[^>]*>(.*?)</dsml parameter>`)

// Some models emit their native tool markup in content. Interpret only one
// bounded invocation; the caller still validates its name against the catalog.
func normalizeToolMarkup(s string) string {
	if !strings.Contains(s, "DSML") {
		return s
	}
	n := strings.ReplaceAll(strings.ReplaceAll(s, "<｜｜DSML｜｜", "<dsml"), "</｜｜DSML｜｜", "</dsml")
	m := dsmlInvoke.FindAllStringSubmatch(n, 2)
	if len(m) != 1 {
		return s
	}
	a := dsmlArguments.FindStringSubmatch(m[0][2])
	if len(a) != 2 || !json.Valid([]byte(strings.TrimSpace(a[1]))) {
		return s
	}
	b, _ := json.Marshal(map[string]any{"action": "tool", "tool": m[0][1], "arguments": json.RawMessage(strings.TrimSpace(a[1]))})
	return string(b)
}
