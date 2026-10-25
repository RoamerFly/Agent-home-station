// Package identity keeps human-readable labels separate from stable identities.
package identity

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type Name struct {
	Text   string
	Source string
}

func Clean(text string) string {
	if !utf8.ValidString(text) {
		return ""
	}
	text = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text))
	if len(text) > 256 {
		text = text[:256]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

func Choose(id, remark, roomName, nickname string) Name {
	for _, pair := range []Name{{remark, "remark"}, {roomName, "room"}, {nickname, "nickname"}} {
		if name := Clean(pair.Text); name != "" && name != id {
			return Name{name, pair.Source}
		}
	}
	return Name{Clean(id), "id"}
}

func Valid(name Name) bool {
	return name.Text != "" && name.Text == Clean(name.Text) && (name.Source == "remark" || name.Source == "room" || name.Source == "nickname" || name.Source == "character")
}
