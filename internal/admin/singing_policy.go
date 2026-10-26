package admin

import (
	"regexp"
	"strings"
)

var vocalGenderTag = regexp.MustCompile(`(?i)\b(?:fe)?male\s+(?:vocals?|singer|voice)\b|男声|女声`)

// Gender is a generation condition, not a guarantee about the rendered singer.
func normalizeSingingStyle(style, gender string) string {
	style = strings.TrimSpace(vocalGenderTag.ReplaceAllString(style, ""))
	if style == "" {
		return ""
	}
	return style + ", " + gender + " vocal"
}
