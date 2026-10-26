package linuxnative

import (
	"encoding/xml"
	"strings"
)

// FileAttachmentName extracts only a file title, never the CDN URL or key.
func (m Message) FileAttachmentName() string {
	if m.Type != 49 || len(m.Body) > 256<<10 {
		return ""
	}
	start := strings.IndexByte(m.Body, '<')
	if start < 0 {
		return ""
	}
	var envelope struct {
		Type  int    `xml:"type"`
		Title string `xml:"title"`
		App   struct {
			Type  int    `xml:"type"`
			Title string `xml:"title"`
		} `xml:"appmsg"`
	}
	if xml.Unmarshal([]byte(m.Body[start:]), &envelope) != nil {
		return ""
	}
	if envelope.App.Type == 6 {
		return strings.TrimSpace(envelope.App.Title)
	}
	if envelope.Type == 6 {
		return strings.TrimSpace(envelope.Title)
	}
	return ""
}
