package bot

import (
	"strings"
	"time"
	"unicode/utf8"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// Record is the locally retained activity requested by the account owner.
// It never contains model reasoning, credentials, or native session material.
type Record struct {
	MessageTime                                                                           string
	RelatedMessageID, DecisionNote                                                        string
	RetryAttempts                                                                         int
	RetryAt                                                                               string
	ToolCacheHit                                                                          bool
	ToolRequests, ToolRetries, ToolHTTPStatus                                             int
	ToolCode, ToolStage                                                                   string
	ModelHTTPStatus, ModelAttempts, ModelMaxAttempts                                      int
	ModelStage                                                                            string
	ModelRetryDelayMS                                                                     int64
	CallID                                                                                string
	Tool, TargetURL, Query                                                                string
	DurationMS, Bytes                                                                     int64
	MediaCount                                                                            int
	Event, Category, ThreadID, Sender, Content, Reason, Status, MessageID, Origin, SendID string
}

func (w *Worker) activity(record Record) {
	if record.MessageID != "" {
		switch record.Event {
		case "message_skipped", "character_decision", "model_failed":
			w.queuePhase(record.MessageID, record.Status)
		case "message_sent":
			w.queuePhase(record.MessageID, "replied")
		case "send_failed":
			if record.Status == "failed" {
				w.queuePhase(record.MessageID, "failed")
			} else {
				w.queuePhase(record.MessageID, "sending")
			}
		case "delivery_stopped":
			w.queuePhase(record.MessageID, record.Status)
		case "delivery_waiting":
			w.queuePhase(record.MessageID, "sending")
		}
	}
	if w.config.Record != nil {
		w.config.Record(record)
	}
}

func (w *Worker) recordIncoming(m linuxnative.Message) {
	if w.runtime != nil {
		for i := range w.runtime.Inbox {
			item := &w.runtime.Inbox[i]
			if item.Event.Data.MessageID == m.ID {
				if item.ReceivedRecorded {
					return
				}
				item.ReceivedRecorded = true
				w.persistErr = w.saveRuntime()
				break
			}
		}
	}
	w.activity(Record{Event: "message_received", Category: "received", ThreadID: m.ThreadID, Sender: m.SenderID, Content: messageSummary(m), MessageID: m.ID, MessageTime: m.CreatedAt, Origin: "wechat", Status: "received"})
}
func messageSummary(m linuxnative.Message) string {
	if m.IsQuotedText() {
		m.Type = 1
		return "[引用回复] " + messageSummary(m)
	}
	if m.Type != 1 {
		switch m.Type {
		case 34:
			return "[收到微信语音]"
		case 3:
			return "[收到图片]"
		case 47:
			return "[收到表情包]"
		case 43:
			return "[收到视频]"
		case 49:
			if name := m.FileAttachmentName(); name != "" {
				return "[文件] " + name
			}
			return "[收到分享卡片/视频号]"
		}
		return "[非文字消息]"
	}
	body := m.Body
	if !utf8.ValidString(body) {
		return "[正文编码无效]"
	}
	if len(body) > 4096 {
		body = body[:4096]
		for !utf8.ValidString(body) {
			body = body[:len(body)-1]
		}
		body += "…[截断]"
	}
	return body
}
func (w *Worker) skipReason(m linuxnative.Message, now time.Time) string {
	if m.Type != 1 {
		return "non_text_message"
	}
	if !utf8.ValidString(m.Body) || len(m.Body) > 4096 {
		return "invalid_or_oversized_text"
	}
	created, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil || now.Sub(created) > w.maxMessageAge() || created.After(now.Add(30*time.Second)) {
		return "message_expired_or_invalid_time"
	}
	if strings.HasPrefix(strings.TrimSpace(m.Body), "[AI]") {
		return "ai_label_loop_prevention"
	}
	if strings.HasSuffix(m.ThreadID, "@chatroom") {
		rule := w.config.GroupRules[m.ThreadID]
		mentioned := m.Mentions.State == "known" && m.Mentions.Self != nil && *m.Mentions.Self
		if rule.Mode == "social" && !mentioned {
			c, ok := w.model.(*companion.Engine)
			if !ok || !c.Config.Permissions.ObserveGroups || !c.Config.Permissions.AutonomousGroups {
				return "social_permission_denied"
			}
		}
		if rule.Mode != "all" && rule.Mode != "social" && !mentioned {
			return "not_a_verified_mention"
		}
		if rule.Prefix != "" {
			return "group_prefix_not_matched_or_empty"
		}
		return "empty_prompt"
	}
	if m.ThreadID == "filehelper" {
		return "private_prefix_not_matched_or_empty"
	}
	return "empty_prompt"
}
