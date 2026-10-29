package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func (p *permittedTools) memoryTools() []mcptools.Tool {
	if !p.manager.Config().Companion.Permissions.Remember {
		return nil
	}
	return []mcptools.Tool{
		agentTool("memory_search", "Search sourced long-term memories or retained originals. Default current conversation; another thread requires verified owner and cross-conversation authorization. Returns previews, source event IDs, coverage, degradation and next_cursor. originals=true searches message originals including indexed archives. No recall is permission to disclose another conversation. Input {query?,thread_id?,kind?,subject?,since?,before?,cursor?,offset?,limit?,originals?}.", `{"type":"object","properties":{"query":{"type":"string"},"thread_id":{"type":"string"},"kind":{"type":"string"},"subject":{"type":"string"},"since":{"type":"string"},"before":{"type":"string"},"cursor":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1},"originals":{"type":"boolean"},"event_year":{"type":"integer","minimum":1,"maximum":9999},"time_basis":{"type":"string","enum":["statement","event"]},"min_similarity":{"type":"number","minimum":-1,"maximum":1}},"additionalProperties":false}`),
		agentTool("memory_read", "Read a memory entry with sources, or an original event using event=true. Read text in UTF-8 byte pages; preserve revision on continuation. The actual transport page is at most32768 bytes; larger requested lengths return effective_length and a continuation. Use source_offset/source_limit to page all citations. Scope is current conversation unless owner explicitly selects another authorized thread. Input {id,thread_id?,event?,offset?,length?,expected_revision?}.", `{"type":"object","properties":{"id":{"type":"string"},"thread_id":{"type":"string"},"event":{"type":"boolean"},"offset":{"type":"integer","minimum":0},"length":{"type":"integer","minimum":1},"expected_revision":{"type":"integer","minimum":1},"source_offset":{"type":"integer","minimum":0},"source_limit":{"type":"integer","minimum":1}},"required":["id"],"additionalProperties":false}`),
		agentTool("memory_remember", "Save a sourced fact/preference/episode/lesson in this conversation. Provide exact original event quotes and the real subject sender ID. Manual notes without event sources require note=true and are attributed to this verified requester, never fabricated as historical fact. Input {kind,detail,summary?,keywords?,subject?,sources?:[{event_id,quote}],note?}.", `{"type":"object","properties":{"kind":{"type":"string","enum":["fact","preference","episode","lesson"]},"detail":{"type":"string"},"summary":{"type":"string"},"keywords":{"type":"array","items":{"type":"string"}},"subject":{"type":"string"},"sources":{"type":"array","items":{"type":"object","properties":{"event_id":{"type":"string"},"quote":{"type":"string"}},"required":["event_id","quote"],"additionalProperties":false}},"note":{"type":"boolean"}},"required":["kind","detail"],"additionalProperties":false}`),
		agentTool("memory_update", "Update an entry owned by this verified requester, or owner. Read first; expected_revision must match. action=forget|pin|unpin|correct. Correction needs new sources; it never changes the original message or task receipts. Forget immediately prevents lexical/semantic injection. Input {id,expected_revision,action,detail?,sources?}.", `{"type":"object","properties":{"id":{"type":"string"},"expected_revision":{"type":"integer","minimum":1},"action":{"type":"string","enum":["forget","pin","unpin","correct"]},"detail":{"type":"string"},"sources":{"type":"array","items":{"type":"object","properties":{"event_id":{"type":"string"},"quote":{"type":"string"}},"required":["event_id","quote"],"additionalProperties":false}}},"required":["id","expected_revision","action"],"additionalProperties":false}`),
	}
}

func (p *permittedTools) memoryScope(thread string) (string, error) {
	if p.trace.Preview || p.trace.ReportOnly || p.trace.Account != p.manager.native.Account() || !p.manager.Config().Companion.Permissions.Remember {
		return "", errors.New("memory permission unavailable")
	}
	if thread == "" {
		thread = p.trace.Thread
	}
	if thread != p.trace.Thread {
		if !p.privileged() || !p.manager.agent.config().CrossConversation {
			return "", errors.New("cross conversation memory denied")
		}
		known, e := p.manager.storedAccountConversations()
		if e != nil {
			return "", e
		}
		if _, ok := known[thread]; !ok {
			return "", errors.New("unknown memory conversation")
		}
	}
	return companion.ScopeID("wechat", p.trace.Account, thread), nil
}

func (p *permittedTools) callMemory(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var in struct {
		companion.MemoryQuery
		Thread       string                   `json:"thread_id"`
		ID           string                   `json:"id"`
		Event        bool                     `json:"event"`
		Length       int                      `json:"length"`
		SourceOffset int                      `json:"source_offset"`
		SourceLimit  int                      `json:"source_limit"`
		Expected     int                      `json:"expected_revision"`
		Detail       string                   `json:"detail"`
		Summary      string                   `json:"summary"`
		Keywords     []string                 `json:"keywords"`
		Sources      []companion.MemorySource `json:"sources"`
		Note         bool                     `json:"note"`
		Action       string                   `json:"action"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", toolInputError("记忆参数无效")
	}
	scope, e := p.memoryScope(in.Thread)
	if e != nil {
		return "", toolInputError(e.Error())
	}
	store := p.manager.memory
	var value any
	switch name {
	case "memory_search":
		in.Scopes = []string{scope}
		if !in.Originals && in.Query != "" {
			cfg, err := p.manager.loadMemoryServices()
			if err != nil {
				return "", err
			}
			if cfg.Embedding.Configured() {
				r, err := cfg.Embedding.Embed(ctx, []string{in.Query})
				tokens := r.Tokens
				if !r.Reported {
					tokens = int64(companion.EstimateTokens(in.Query))
				}
				_ = store.RecordMaintenanceUsage(context.WithoutCancel(ctx), "explicit_recall_embedding", tokens, r.Reported)
				if err == nil {
					in.Vector = r.Vectors[0]
					in.Space = r.Space
				}
			}
		}
		value, e = store.SearchMemory(ctx, in.MemoryQuery)
	case "memory_read":
		if in.Event {
			ev, err := store.ReadEvent(ctx, []string{scope}, in.ID)
			if err != nil {
				return "", localAgentError(err)
			}
			if in.Expected > 0 && in.Expected != ev.Revision {
				return "", companion.ErrMemoryConflict
			}
			if in.Offset < 0 || in.Offset > len(ev.Text) || in.Offset > 0 && in.Offset < len(ev.Text) && !utf8.RuneStart(ev.Text[in.Offset]) || in.Length < 0 {
				return "", toolInputError("原文分页位置无效")
			}
			length := in.Length
			if length == 0 {
				length = 4000
			}
			requested := length
			length = min(length, 32768)
			end := in.Offset + min(length, len(ev.Text)-in.Offset)
			for end > in.Offset && end < len(ev.Text) && !utf8.RuneStart(ev.Text[end]) {
				end--
			}
			if end == in.Offset && end < len(ev.Text) {
				return "", toolInputError("分页长度不足以容纳一个UTF-8字符")
			}
			total := len(ev.Text)
			ev.Text = ev.Text[in.Offset:end]
			value = map[string]any{"event": ev, "total_bytes": total, "next_offset": end, "has_more": end < total, "revision": ev.Revision, "requested_length": requested, "effective_length": end - in.Offset, "transport_page_bytes": 32768}
		} else {
			m, err := store.ReadMemory(ctx, []string{scope}, in.ID)
			if err != nil {
				return "", localAgentError(err)
			}
			if in.Expected > 0 && in.Expected != m.Version {
				return "", companion.ErrMemoryConflict
			}
			if in.Offset < 0 || in.Offset > len(m.Detail) || in.Length < 0 || in.SourceOffset < 0 || in.SourceOffset > len(m.Sources) || in.SourceLimit < 0 {
				return "", toolInputError("记忆分页位置无效")
			}
			if in.Offset < len(m.Detail) && in.Offset > 0 && !utf8.RuneStart(m.Detail[in.Offset]) {
				return "", toolInputError("分页位置不在UTF-8边界")
			}
			length := in.Length
			if length == 0 {
				length = 4000
			}
			requested := length
			length = min(length, 32768)
			end := in.Offset + min(length, len(m.Detail)-in.Offset)
			for end > in.Offset && end < len(m.Detail) && !utf8.RuneStart(m.Detail[end]) {
				end--
			}
			if end == in.Offset && end < len(m.Detail) {
				return "", toolInputError("分页长度不足以容纳一个UTF-8字符")
			}
			total := len(m.Detail)
			m.Detail = m.Detail[in.Offset:end]
			sourceLimit := in.SourceLimit
			if sourceLimit == 0 {
				sourceLimit = 20
			}
			sourceEnd := in.SourceOffset + min(sourceLimit, len(m.Sources)-in.SourceOffset)
			sourceTotal := len(m.Sources)
			m.Sources = m.Sources[in.SourceOffset:sourceEnd]
			value = map[string]any{"entry": m, "total_bytes": total, "next_offset": end, "has_more": end < total, "revision": m.Version, "requested_length": requested, "effective_length": end - in.Offset, "source_count": sourceTotal, "next_source_offset": sourceEnd, "source_has_more": sourceEnd < sourceTotal, "transport_page_bytes": 32768}
		}
	case "memory_remember":
		if in.Subject == "" {
			in.Subject = p.trace.Sender
		}
		if in.Subject != p.trace.Sender && !p.privileged() {
			return "", toolInputError("只能保存自己的事实或笔记")
		}
		if in.Note {
			in.Subject = p.trace.Sender
		}
		value, e = store.SaveMemory(ctx, companion.MemoryEntry{Scope: scope, Subject: in.Subject, Kind: in.Kind, Detail: in.Detail, Summary: in.Summary, Keywords: in.Keywords, Sources: in.Sources, Manual: in.Note}, 0)
	case "memory_update":
		m, err := store.ReadMemory(ctx, []string{scope}, in.ID)
		if err != nil {
			return "", localAgentError(err)
		}
		if m.Subject != p.trace.Sender && !p.privileged() {
			return "", toolInputError("不能修改其他人的长期记忆")
		}
		if in.Expected < 1 {
			return "", toolInputError("请先读取记忆版本")
		}
		switch in.Action {
		case "forget":
			m.Status = "deleted"
		case "pin":
			m.Pinned = true
		case "unpin":
			m.Pinned = false
		case "correct":
			if strings.TrimSpace(in.Detail) == "" {
				return "", toolInputError("更正内容为空")
			}
			m.Detail = in.Detail
			m.Summary = in.Detail
			m.Sources = in.Sources
			if len(in.Sources) == 0 && !m.Manual {
				return "", toolInputError("更正需要新来源")
			}
		default:
			return "", toolInputError("未知记忆操作")
		}
		value, e = store.SaveMemory(ctx, m, in.Expected)
	default:
		return "", toolInputError("未知记忆工具")
	}
	if e != nil {
		return "", localAgentError(e)
	}
	if _, err := p.memoryScope(in.Thread); err != nil {
		return "", toolInputError("记忆权限已撤销")
	}
	b, e := json.Marshal(value)
	return string(b), e
}
