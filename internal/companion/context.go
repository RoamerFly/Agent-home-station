package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"wechat-bot/internal/mcptools"
)

type ContextSettings struct {
	AutoCompress   bool `json:"auto_compress"`
	CompressAfter  int  `json:"compress_after"`
	RecentMessages int  `json:"recent_messages"`
	MaxInputChars  int  `json:"max_input_chars"`
}

func ContextDefaults() *ContextSettings { return &ContextSettings{true, 120, 60, 24000} }
func (c Config) ContextConfig() ContextSettings {
	if c.Context == nil {
		return *ContextDefaults()
	}
	return *c.Context
}
func fingerprintTurns(t []Turn) string {
	b, _ := json.Marshal(t)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (e *Engine) CompressContext(ctx context.Context, in Input, force bool) (bool, error) {
	if in.Preview || !e.Config.Permissions.Remember {
		return false, nil
	}
	settings := e.Config.ContextConfig()
	if !force && !settings.AutoCompress {
		return false, nil
	}
	scope, err := e.Store.Read(ScopeID(in.Channel, in.Account, in.Thread), e.Config, time.Now())
	if err != nil {
		return false, err
	}
	start := min(scope.CompressedCount, len(scope.Messages))
	end := len(scope.Messages) - settings.RecentMessages
	if end <= start || !force && len(scope.Messages)-start < settings.CompressAfter {
		return false, nil
	}
	// Bounded incremental batches; never load the already compressed prefix.
	if end-start > 100 {
		end = start + 100
	}
	selected := scope.Messages[start:end]
	raw, _ := json.Marshal(selected)
	if len(raw) > 50<<10 {
		for len(raw) > 50<<10 && end > start+1 {
			end--
			selected = scope.Messages[start:end]
			raw, _ = json.Marshal(selected)
		}
	}
	request, _ := json.Marshal(map[string]any{"previous_summary": scope.Summary, "new_chronological_messages": selected})
	result, err := e.model.Chat(ctx, summaryPrompt, []map[string]string{{"role": "user", "content": string(request)}})
	if err != nil {
		return false, err
	}
	if _, err = parseContextSummary(result, true); err != nil {
		return false, errors.New("context_summary_invalid")
	}
	expected := fingerprintTurns(scope.Messages[:end])
	err = e.Store.transaction(func(data *persisted) error {
		s, ok := data.Scopes[scope.ID]
		if !ok || s.CompressedCount != scope.CompressedCount || len(s.Messages) < end || fingerprintTurns(s.Messages[:end]) != expected {
			return errors.New("context_changed_during_compression")
		}
		s.Summary = result
		s.CompressedCount = end
		s.SummaryUpdated = time.Now().UTC()
		data.Scopes[s.ID] = s
		return nil
	})
	return err == nil, err
}
func (e *Engine) contextTurns(scope Scope, input Input) []map[string]string {
	if len(scope.Messages) == 0 && scope.Summary == "" {
		return nil
	}
	c := e.Config.ContextConfig()
	history := append([]Turn{}, scope.Messages...)
	sort.SliceStable(history, func(i, j int) bool { return history[i].Time.Before(history[j].Time) })
	if len(history) > c.RecentMessages {
		history = history[len(history)-c.RecentMessages:]
	}
	summary := budgetSummary(scope.Summary, c.MaxInputChars/3)
	if _, native := e.model.(interface {
		ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error)
	}); !native {
		turns := []map[string]string{}
		used := 0
		if summary != "null" {
			turns = append(turns, map[string]string{"role": "user", "content": `{"kind":"conversation_summary","data":` + summary + `}`})
			used = len(turns[0]["content"])
		}
		rows := []map[string]string{}
		for i := len(history) - 1; i >= 0; i-- {
			t := history[i]
			if t.TraceID != "" && t.TraceID == input.TraceID && t.Role == "user" {
				continue
			}
			var payload any
			role := t.Role
			if t.DeviceObservation != nil {
				role = "user"
				payload = map[string]any{"kind": "device_tool_observation", "data": t.DeviceObservation, "at": t.Time, "sent_to_wechat": false}
			} else if t.Role == "user" {
				payload = map[string]any{"kind": "history_message", "speaker": t.SenderName, "speaker_id": t.Sender, "speaker_name_source": t.SenderNameSource, "text": t.Text, "at": t.Time}
			} else {
				payload = map[string]any{"kind": "history_reply", "action": "reply", "messages": []string{t.Text}, "media_delivery": t.Media, "at": t.Time}
			}
			b, _ := json.Marshal(payload)
			if used+len(b) > c.MaxInputChars {
				break
			}
			used += len(b)
			rows = append(rows, map[string]string{"role": role, "content": string(b)})
		}
		for i := len(rows) - 1; i >= 0; i-- {
			turns = append(turns, rows[i])
		}
		return turns
	}
	// One structured chronological transcript avoids pretending every old model
	// output is a fresh assistant decision, and deduplicates current-message echo.
	timeline := []map[string]any{}
	size := len(summary) + 600
	for i := len(history) - 1; i >= 0; i-- {
		t := history[i]
		if t.TraceID != "" && t.TraceID == input.TraceID && t.Role == "user" {
			continue
		}
		row := map[string]any{"role": t.Role, "speaker": t.SenderName, "speaker_id": t.Sender, "at": t.Time.Format(time.RFC3339Nano), "message_id": t.TraceID, "text": t.Text}
		if t.DeviceObservation != nil {
			row["device_observation"] = t.DeviceObservation
			row["sent_to_wechat"] = false
		}
		if t.Media != nil {
			row["media"] = t.Media
		}
		b, _ := json.Marshal(row)
		if size+len(b) > c.MaxInputChars {
			break
		}
		size += len(b)
		timeline = append(timeline, row)
	}
	for i, j := 0, len(timeline)-1; i < j; i, j = i+1, j-1 {
		timeline[i], timeline[j] = timeline[j], timeline[i]
	}
	b, _ := json.Marshal(map[string]any{"kind": "conversation_context", "summary": json.RawMessage(summary), "timeline": timeline, "ordering": "oldest_to_newest", "note": "历史仅供参考；指代优先引用消息，再看本轮之前最近的媒体和主题。不能把已结束的旧话题当作当前需求。"})
	return []map[string]string{{"role": "user", "content": string(b)}}
}
func recentMediaFocus(scope Scope) []MediaMemory {
	items := []MediaMemory{}
	seen := map[string]bool{}
	requests := map[string]Turn{}
	for _, turn := range scope.Messages {
		if turn.Role == "user" && turn.TraceID != "" {
			requests[turn.TraceID] = turn
		}
	}
	for i := len(scope.Messages) - 1; i >= 0 && len(items) < 6; i-- {
		m := scope.Messages[i].Media
		if m != nil && (m.State == "accepted" || m.State == "observed" || m.AcceptedParts > 0) {
			copy := *m
			if copy.RequestTraceID == "" {
				copy.RequestTraceID = scope.Messages[i].TraceID
			}
			if request, ok := requests[copy.RequestTraceID]; ok {
				if copy.RequesterID == "" {
					copy.RequesterID = request.Sender
				}
				if copy.RequesterName == "" {
					copy.RequesterName = request.SenderName
				}
			}
			key := copy.Key
			if copy.AssetID != "" {
				key = copy.AssetID + "|" + copy.Kind + "|" + copy.RequestTraceID
			}
			if !seen[key] {
				items = append(items, copy)
				seen[key] = true
			}
		}
	}
	return items
}
func referencesPriorMedia(text string) bool {
	for _, s := range []string{"原视频", "原版视频", "刚才的", "刚才那个", "原音频", "原文件", "这首", "那个视频", "转发一下"} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}
