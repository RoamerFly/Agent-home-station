package linuxnative

import (
	"encoding/xml"
	"strings"
)

// FileAttachmentName extracts only a file title, never the CDN URL or key.
func (m Message) FileAttachmentName() string {
	// A known native subtype takes precedence over embedded XML. Retain XML
	// fallback for older projections that did not expose appMessageType.
	if m.Type != 49 || m.AppMessageType != nil && *m.AppMessageType != 6 {
		return ""
	}
	if m.Type == 49 && m.AppMessageType != nil && *m.AppMessageType == 6 {
		for _, a := range m.Attachments {
			if a.Kind == "file" && a.Filename != "" {
				return a.Filename
			}
		}
	}
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
