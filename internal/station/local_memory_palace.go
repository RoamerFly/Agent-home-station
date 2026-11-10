package station

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/websession"
)

type palaceMemory struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Commitment string `json:"commitment,omitempty"`
}

func palaceMemoryOf(m companion.MemoryEntry) palaceMemory {
	title := strings.TrimSpace(m.Summary)
	if title == "" {
		title = m.Subject
	}
	return palaceMemory{ID: m.ID, Title: title, Kind: m.Kind, Status: m.Status, Commitment: m.CommitmentState}
}

func (h *localChatService) memoryPalace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, in, err := h.conversation()
	if err != nil {
		websession.Fail(w, http.StatusConflict, err.Error())
		return
	}
	query := r.URL.Query()
	scope := []string{companion.ScopeID(in.Channel, in.Account, in.Thread)}
	if id := r.PathValue("id"); id != "" {
		current, history, err := store.MemoryHistory(r.Context(), scope, id)
		if errors.Is(err, sql.ErrNoRows) {
			websession.Fail(w, http.StatusNotFound, "这条记忆已更新或在当前会话中不可读")
			return
		}
		if err != nil {
			websession.Fail(w, http.StatusServiceUnavailable, "暂时无法读取记忆详情")
			return
		}
		type source struct {
			Quote string `json:"quote"`
			At    string `json:"at,omitempty"`
		}
		type revision struct {
			Version int    `json:"version"`
			Title   string `json:"title"`
		}
		sources := make([]source, 0, len(current.Sources))
		for _, ref := range current.Sources {
			event, err := store.ReadEvent(r.Context(), scope, ref.EventID)
			if err == nil && ref.Quote != "" && strings.Contains(event.Text, ref.Quote) {
				sources = append(sources, source{Quote: ref.Quote, At: event.At})
			}
		}
		revisions := make([]revision, 0, len(history))
		for _, old := range history {
			revisions = append(revisions, revision{Version: old.Version, Title: palaceMemoryOf(old).Title})
		}
		websession.Respond(w, http.StatusOK, map[string]any{
			"memory": palaceMemoryOf(current), "detail": current.Detail, "version": current.Version,
			"sources": sources, "revisions": revisions,
		})
		return
	}
	search := companion.MemoryQuery{Scopes: scope, Limit: 5, Cursor: query.Get("cursor")}
	switch kind := query.Get("kind"); kind {
	case "", "fact", "episode", "preference":
		search.Kind = kind
	case "commitment":
		search.CommitmentState = "open"
	default:
		websession.Fail(w, http.StatusBadRequest, "记忆类别无效")
		return
	}
	page, err := store.SearchMemory(r.Context(), search)
	if errors.Is(err, companion.ErrMemoryConflict) {
		websession.Fail(w, http.StatusConflict, "记忆已更新，请从第一页重新读取")
		return
	}
	if err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, "暂时无法读取记忆宫殿")
		return
	}
	items := make([]palaceMemory, 0, len(page.Items))
	for _, hit := range page.Items {
		if hit.Entry != nil {
			items = append(items, palaceMemoryOf(*hit.Entry))
		}
	}
	websession.Respond(w, http.StatusOK, map[string]any{"items": items, "next_cursor": page.NextCursor})
}
