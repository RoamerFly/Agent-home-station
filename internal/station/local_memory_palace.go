package station

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"wechat-bot/internal/apphost"
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
	var proof *localWechatMemoryProof
	var tools *localToolSet
	if query.Get("app_id") != "" {
		if query.Get("app_id") != "wechat" || h.wechat == nil {
			websession.Fail(w, http.StatusForbidden, "该记忆来源当前不可读取")
			return
		}
		source := localWechatMemorySource{Connection: query.Get("connection_id"), Account: query.Get("account_id"), Thread: query.Get("thread_id"), Dataset: query.Get("dataset")}
		expected := localWechatMemoryProof{Source: source}
		var selected localWechatMemoryProof
		store, selected, err = h.wechat.openLocalWechatMemory(r.Context(), in.Account, in.Sender, expected)
		if err != nil {
			palaceSourceFailure(w, err)
			return
		}
		defer store.Close()
		proof = &selected
		if version := query.Get("scope_version"); version != "" && version != localCheckpointKey(selected) || r.PathValue("id") != "" && query.Get("scope_version") == "" {
			palaceSourceFailure(w, companion.ErrMemoryConflict)
			return
		}
		scope = []string{companion.ScopeID("wechat", source.Account, source.Thread)}
		tools = &localToolSet{wechat: h.wechat, access: &localWorkspaceAccess{home: h.home, fishID: in.Account, ownerID: in.Sender},
			traceID: localCheckpointKey("memory-palace", r.URL.Path, query.Encode())}
		if source.Dataset == "archive" {
			value, count, err := h.archivePalace(r.Context(), store, selected, r)
			if err != nil {
				palaceSourceFailure(w, err)
				return
			}
			h.palaceResponse(w, r, value, selected, tools, count)
			return
		}
	} else if query.Get("connection_id") != "" || query.Get("account_id") != "" || query.Get("thread_id") != "" || query.Get("dataset") != "" {
		palaceSourceFailure(w, apphost.ErrObservationInvalid)
		return
	}
	respond := func(value map[string]any, count int) {
		if proof != nil {
			h.palaceResponse(w, r, value, *proof, tools, count)
			return
		}
		_, current, err := h.conversation()
		if err != nil || current.Account != in.Account || current.Sender != in.Sender {
			palaceSourceFailure(w, apphost.ErrObservationForbidden)
			return
		}
		websession.Respond(w, http.StatusOK, value)
	}
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
		respond(map[string]any{
			"memory": palaceMemoryOf(current), "detail": current.Detail, "version": current.Version,
			"sources": sources, "revisions": revisions,
		}, 1)
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
	respond(map[string]any{"items": items, "next_cursor": page.NextCursor}, len(items))
}

func (h *localChatService) memoryPalaceSources(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, in, err := h.conversation()
	if err != nil {
		palaceSourceFailure(w, err)
		return
	}
	sources := []localWechatMemorySource{}
	if h.wechat != nil {
		sources, err = h.wechat.localMemorySources(r.Context(), in.Account, in.Sender)
		if err != nil {
			palaceSourceFailure(w, err)
			return
		}
	}
	_, current, err := h.conversation()
	if err != nil || current.Account != in.Account || current.Sender != in.Sender {
		palaceSourceFailure(w, apphost.ErrObservationForbidden)
		return
	}
	websession.Respond(w, http.StatusOK, map[string]any{"app_id": "wechat", "sources": sources})
}

func palaceSourceFailure(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := "记忆来源暂不可读取，请刷新来源后核对授权。"
	if errors.Is(err, apphost.ErrObservationForbidden) {
		status, message = http.StatusForbidden, "当前来源未获授权，请重新选择。"
	} else if errors.Is(err, companion.ErrMemoryConflict) {
		status, message = http.StatusConflict, "记忆来源或授权已变化，请从第一页重新读取。"
	} else if errors.Is(err, apphost.ErrObservationInvalid) {
		status, message = http.StatusBadRequest, "记忆来源或分页参数无效。"
	}
	websession.Fail(w, status, message)
}

func (h *localChatService) palaceResponse(w http.ResponseWriter, r *http.Request, value map[string]any, proof localWechatMemoryProof, tools *localToolSet, count int) {
	store, _, err := h.wechat.openLocalWechatMemory(r.Context(), tools.access.fishID, tools.access.ownerID, proof)
	if err != nil {
		palaceSourceFailure(w, err)
		return
	}
	store.Close()
	value["source"], value["scope_version"] = proof.Source, localCheckpointKey(proof)
	body, err := localMemoryJSON(value)
	if err == nil {
		err = tools.auditWechatMemory(proof, r.PathValue("id") == "", count, body)
	}
	if err != nil {
		palaceSourceFailure(w, err)
		return
	}
	websession.Respond(w, http.StatusOK, value)
}

func (h *localChatService) archivePalace(ctx context.Context, store *companion.Store, proof localWechatMemoryProof, r *http.Request) (map[string]any, int, error) {
	records, err := h.wechat.wechatArchiveRecords(ctx, store, proof)
	if err != nil {
		return nil, 0, err
	}
	query := r.URL.Query()
	if id := r.PathValue("id"); id != "" {
		parse := func(name string) (*int, error) {
			if !query.Has(name) {
				return nil, nil
			}
			value, err := strconv.Atoi(query.Get(name))
			if err != nil || value < 0 {
				return nil, apphost.ErrObservationInvalid
			}
			return &value, nil
		}
		offset, offsetErr := parse("offset")
		length, lengthErr := parse("length")
		sourceIndex, sourceErr := parse("source_index")
		sourceOffset, sourceOffsetErr := parse("source_offset")
		sourceLimit, sourceLimitErr := parse("source_limit")
		if offsetErr != nil || lengthErr != nil || sourceErr != nil || sourceOffsetErr != nil || sourceLimitErr != nil {
			return nil, 0, apphost.ErrObservationInvalid
		}
		for _, record := range records {
			if record.ID == id {
				value, err := archiveMemoryDetail(record, sourceIndex, localMemoryReadInput{ID: record.ID, Offset: offset, Length: length, SourceOffset: sourceOffset, SourceLimit: sourceLimit})
				return value, 1, err
			}
		}
		return nil, 0, companion.ErrMemoryConflict
	}
	kind := query.Get("kind")
	if kind != "" && kind != "fact" && kind != "episode" && kind != "preference" && kind != "commitment" {
		return nil, 0, apphost.ErrObservationInvalid
	}
	page, cursor, err := archiveMemoryPage(records, proof, "", "", "", kind, query.Get("cursor"), 5)
	if err != nil {
		return nil, 0, err
	}
	items := []palaceMemory{}
	for _, record := range page {
		items = append(items, palaceMemory{ID: record.ID, Title: archivedMemoryPreview(record), Kind: record.Kind, Status: "archived"})
	}
	return map[string]any{"items": items, "next_cursor": cursor, "record_origin": "legacy_import_snapshot"}, len(items), nil
}
