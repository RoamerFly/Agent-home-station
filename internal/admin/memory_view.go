package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
	"wechat-bot/internal/companion"
)

func newestMemory(messages []companion.Turn) []companion.Turn {
	copy := append([]companion.Turn{}, messages...)
	// Reverse first so equal timestamps keep the newest insertion first.
	for i, j := 0, len(copy)-1; i < j; i, j = i+1, j-1 {
		copy[i], copy[j] = copy[j], copy[i]
	}
	sort.SliceStable(copy, func(i, j int) bool { return copy[i].Time.After(copy[j].Time) })
	return copy
}

func memoryDetailView(scope companion.Scope) map[string]any {
	folded := scope.FoldedHistoryCount()
	return map[string]any{
		"id": scope.ID, "channel": scope.Channel, "thread": scope.Thread,
		"messages": newestMemory(scope.Messages[folded:]), "notes": scope.Notes,
		"preferences": scope.Preferences, "preferences_revision": scope.PreferencesRevision,
		"summary": scope.Summary, "summary_updated": scope.SummaryUpdated,
		"compressed_count": scope.CompressedCount, "history_count": folded,
		"retained_context_count": max(0, folded-scope.CompressedCount),
		"total_messages":         len(scope.Messages), "last_interaction": scope.LastInteraction,
	}
}

func (s *Server) memoryDetail(w http.ResponseWriter, r *http.Request) {
	scope, err := s.manager.memoryScope(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	view := memoryDetailView(scope)
	visible := scope
	visible.Messages = scope.Messages[scope.FoldedHistoryCount():]
	visible, err = s.manager.namedMemory(r.Context(), visible)
	if err != nil {
		fail(w, 500, "本地记忆名称记录失败，原记忆已保留")
		return
	}
	view["messages"] = newestMemory(visible.Messages)
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	view["label"] = s.manager.memoryLabel(ctx, scope.Thread)
	respond(w, 200, view)
}

func foldedRevision(messages []companion.Turn) string {
	copy := append([]companion.Turn{}, messages...)
	for i := range copy {
		copy[i].SenderName, copy[i].SenderNameSource = "", ""
	}
	b, _ := json.Marshal(copy)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func (s *Server) memoryHistory(w http.ResponseWriter, r *http.Request) {
	scope, err := s.manager.memoryScope(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			fail(w, 400, "历史分页位置无效")
			return
		}
	}
	folded := scope.FoldedHistoryCount()
	revision := foldedRevision(scope.Messages[:folded])
	if expected := r.URL.Query().Get("revision"); expected != "" && expected != revision {
		fail(w, 409, "历史已更新，请重新打开历史记录；未修改任何消息")
		return
	}
	if offset > folded {
		fail(w, 400, "历史分页位置超出范围")
		return
	}
	ordered := newestMemory(scope.Messages[:folded])
	end := min(offset+100, folded)
	page := scope
	page.Messages = ordered[offset:end]
	page, err = s.manager.namedMemory(r.Context(), page)
	if err != nil {
		fail(w, 500, "历史名称读取失败")
		return
	}
	var next *int
	if end < folded {
		next = &end
	}
	respond(w, 200, map[string]any{"messages": page.Messages, "total": folded, "compressed_count": scope.CompressedCount, "retained_context_count": max(0, folded-scope.CompressedCount), "next_offset": next, "revision": revision, "read_only": true})
}
