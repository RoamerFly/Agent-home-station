package bot

import (
	"strings"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// The model interprets administrator-authored free text in its existing turn;
// no keyword-to-permission conversion or extra classification model is used.
func (w *Worker) considerPreferredShare(m linuxnative.Message) bool {
	if m.Type != 49 || m.IsQuotedText() || m.FileAttachmentName() != "" || (m.Link == nil && (m.AppMessageType == nil || *m.AppMessageType != 51)) || m.Direction != "incoming" || !strings.HasSuffix(m.ThreadID, "@chatroom") || !w.allowed[m.ThreadID] || w.config.Analyzer == nil || w.config.Options == nil || !w.config.Options.ReadVideos {
		return false
	}
	e, ok := w.model.(*companion.Engine)
	if !ok || !e.Config.Enabled || !e.Config.Permissions.ObserveGroups || !e.Config.Permissions.AutonomousGroups || !e.Config.Permissions.ReplyText || e.Store == nil {
		return false
	}
	text, _ := e.Store.Preferences(companion.ScopeID(w.config.Channel, w.source.Account(), m.ThreadID))
	return strings.TrimSpace(text) != ""
}
