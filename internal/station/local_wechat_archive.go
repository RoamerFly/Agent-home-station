package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/workspacefs"
)

func (s *stationWechat) localMemorySources(ctx context.Context, fish, owner string) ([]localWechatMemorySource, error) {
	connections, err := s.Connections(ctx)
	if err != nil {
		return nil, err
	}
	sources := []localWechatMemorySource{}
	for _, connection := range connections {
		worker, err := s.worker(connection.ID, false)
		if err != nil || worker == nil {
			continue
		}
		worker.ownerMu.Lock()
		grants, err := readStationMemoryGrants(worker, connection.AccountID)
		worker.ownerMu.Unlock()
		if err != nil {
			continue
		}
		for thread := range grants.StationReads {
			if _, _, err := s.memoryGrant(connection.ID, connection.AccountID, thread, fish, owner); err != nil {
				continue
			}
			source := localWechatMemorySource{Connection: connection.ID, Account: connection.AccountID, Thread: thread}
			sources = append(sources, source)
			// Discovery checks only the archive file's presence and type.
			root, err := workspacefs.OpenPath(worker.dir)
			if err == nil {
				info, statErr := root.Lstat(stationLegacyMemoryFile(connection.AccountID, thread))
				root.Close()
				if statErr == nil && info.Mode().IsRegular() {
					source.Dataset = "archive"
					sources = append(sources, source)
				}
			}
		}
	}
	slices.SortFunc(sources, func(a, b localWechatMemorySource) int {
		return strings.Compare(a.Connection+"\x00"+a.Thread+"\x00"+a.Dataset, b.Connection+"\x00"+b.Thread+"\x00"+b.Dataset)
	})
	return sources, ctx.Err()
}

func (s *stationWechat) localWechatProof(ctx context.Context, store *companion.Store, fish, owner string, source localWechatMemorySource) (localWechatMemoryProof, error) {
	if source.Dataset != "" && source.Dataset != "archive" {
		return localWechatMemoryProof{}, apphost.ErrObservationInvalid
	}
	grant, stamp, err := s.memoryGrant(source.Connection, source.Account, source.Thread, fish, owner)
	if err != nil {
		return localWechatMemoryProof{}, err
	}
	epoch, err := store.RecallEpoch(ctx)
	if err != nil {
		return localWechatMemoryProof{}, err
	}
	proof := localWechatMemoryProof{Source: source, Revision: grant.Revision, Grant: stamp, Instance: store.SourceInstanceID(), Epoch: epoch}
	if source.Dataset == "archive" {
		worker, err := s.worker(source.Connection, false)
		if err != nil || worker == nil || !store.LegacyHistoryAllowed(source.Account, source.Thread) {
			return localWechatMemoryProof{}, apphost.ErrObservationForbidden
		}
		archive, err := readStationLegacyMemory(worker.dir, source.Account, source.Thread, stamp)
		if err != nil {
			return localWechatMemoryProof{}, err
		}
		proof.Archive = localCheckpointKey(archive)
		records, err := s.wechatArchiveRecords(ctx, store, proof)
		if err != nil {
			return localWechatMemoryProof{}, err
		}
		proof.ArchiveVisibility = localCheckpointKey(records)
	}
	if s.ownerReader == nil {
		return localWechatMemoryProof{}, apphost.ErrObservationForbidden
	}
	current, err := s.ownerReader()
	if err != nil || current.FishID != fish || current.ID != owner {
		return localWechatMemoryProof{}, apphost.ErrObservationForbidden
	}
	return proof, nil
}

func (s *stationWechat) openLocalWechatMemory(ctx context.Context, fish, owner string, expected localWechatMemoryProof) (*companion.Store, localWechatMemoryProof, error) {
	_, stamp, err := s.memoryGrant(expected.Source.Connection, expected.Source.Account, expected.Source.Thread, fish, owner)
	if err != nil {
		return nil, localWechatMemoryProof{}, err
	}
	dir, err := stationMemoryDirectory(s.root, expected.Source.Connection)
	if err != nil && expected.Source.Dataset == "archive" {
		worker, workerErr := s.worker(expected.Source.Connection, false)
		if workerErr != nil || worker == nil {
			return nil, localWechatMemoryProof{}, apphost.ErrUnavailable
		}
		if _, archiveErr := readStationLegacyMemory(worker.dir, expected.Source.Account, expected.Source.Thread, stamp); archiveErr != nil {
			return nil, localWechatMemoryProof{}, archiveErr
		}
		dir = filepath.Join(worker.dir, "memory")
		root, createErr := workspacefs.EnsurePath(dir)
		if createErr != nil {
			return nil, localWechatMemoryProof{}, createErr
		}
		root.Close()
	} else if err != nil {
		return nil, localWechatMemoryProof{}, apphost.ErrUnavailable
	}
	store, err := companion.NewStore(dir)
	if err != nil {
		return nil, localWechatMemoryProof{}, err
	}
	proof, err := s.localWechatProof(ctx, store, fish, owner, expected.Source)
	if err == nil && expected.Revision != 0 && expected != proof {
		err = companion.ErrMemoryConflict
	}
	if err != nil {
		store.Close()
		return nil, localWechatMemoryProof{}, err
	}
	return store, proof, nil
}

type localWechatArchiveRecord struct {
	ID, Kind, Text, At string
	Version            int
	Sources            []companion.LegacyMemoryEvent
	Original           bool
}

// Every legacy source is checked against the live forget/correction barrier.
// This projection never writes imported text into a live conversation store.
func (s *stationWechat) wechatArchiveRecords(ctx context.Context, store *companion.Store, proof localWechatMemoryProof) ([]localWechatArchiveRecord, error) {
	worker, err := s.worker(proof.Source.Connection, false)
	if err != nil || worker == nil || !store.LegacyHistoryAllowed(proof.Source.Account, proof.Source.Thread) {
		return nil, apphost.ErrObservationForbidden
	}
	item, err := readStationLegacyMemory(worker.dir, proof.Source.Account, proof.Source.Thread, proof.Grant)
	if err != nil {
		return nil, err
	}
	if item.SourceAccount != "" {
		native, err := s.WorkerSender(ctx, proof.Source.Connection)
		if err != nil || !s.mappedArchiveAllowed(ctx, native, item) {
			return nil, apphost.ErrObservationForbidden
		}
	}
	if proof.Archive == "" || proof.Archive != localCheckpointKey(item) {
		return nil, companion.ErrMemoryConflict
	}
	events := map[string]companion.LegacyMemoryEvent{}
	visible := map[string]bool{}
	for _, event := range item.Selection.Events {
		if _, exists := events[event.EventID]; exists {
			return nil, companion.ErrMemoryConflict
		}
		allowed, err := legacyEventRecallAllowed(ctx, store, proof.Source.Account, proof.Source.Thread, event)
		if err != nil {
			return nil, err
		}
		events[event.EventID], visible[event.EventID] = event, allowed
	}
	records := []localWechatArchiveRecord{}
	ids := map[string]bool{}
	for _, fact := range item.Selection.Memories {
		if fact.ID == "" || ids[fact.ID] || fact.Version < 1 || strings.TrimSpace(fact.Text) == "" || !utf8.ValidString(fact.Text) || len(fact.Sources) == 0 {
			continue
		}
		ids[fact.ID] = true
		record := localWechatArchiveRecord{ID: "archive-fact-" + localCheckpointKey(fact.ID), Kind: fact.Kind, Text: fact.Text, Version: fact.Version}
		seen, valid := map[string]bool{}, true
		for _, source := range fact.Sources {
			event, exists := events[source.EventID]
			if !exists || !visible[source.EventID] || seen[source.EventID] || event.Revision != source.Revision {
				valid = false
				break
			}
			seen[source.EventID] = true
			record.Sources = append(record.Sources, event)
			at, _ := time.Parse(time.RFC3339Nano, event.OccurredAt)
			previous, _ := time.Parse(time.RFC3339Nano, record.At)
			if at.After(previous) {
				record.At = event.OccurredAt
			}
		}
		if valid {
			records = append(records, record)
		}
	}
	for _, event := range item.Selection.Events {
		if visible[event.EventID] {
			records = append(records, localWechatArchiveRecord{ID: "archive-event-" + localCheckpointKey(event.EventID), Kind: "episode",
				Text: event.Text, At: event.OccurredAt, Version: event.Revision, Sources: []companion.LegacyMemoryEvent{event}, Original: true})
		}
	}
	return records, ctx.Err()
}

type localWechatArchiveCursor struct {
	Version, Query, Since, Before, Kind string
	Offset                              int
}

func archiveMemoryPage(records []localWechatArchiveRecord, proof localWechatMemoryProof, query, since, before, kind, cursor string, limit int) ([]localWechatArchiveRecord, string, error) {
	page := localWechatArchiveCursor{Version: localCheckpointKey(proof), Query: query, Since: since, Before: before, Kind: kind}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		var previous localWechatArchiveCursor
		if err != nil || len(raw) > 4096 || strictLocalMemoryDecode(raw, &previous) != nil || previous.Offset < 0 {
			return nil, "", companion.ErrMemoryConflict
		}
		page.Offset = previous.Offset
		if previous != page {
			return nil, "", companion.ErrMemoryConflict
		}
	}
	start, err := douyinMemoryDate(since)
	if err != nil {
		return nil, "", err
	}
	end, err := douyinMemoryDate(before)
	if err != nil || start != "" && end != "" && start >= end {
		return nil, "", apphost.ErrObservationInvalid
	}
	filtered := []localWechatArchiveRecord{}
	terms := strings.Fields(strings.ToLower(query))
	for _, record := range records {
		if kind != "" && record.Kind != kind {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, record.At)
		if err != nil {
			continue
		}
		stamp := at.UTC().Format(localDouyinMemoryTime)
		if start != "" && stamp < start || end != "" && stamp >= end {
			continue
		}
		match := true
		for _, term := range terms {
			match = match && strings.Contains(strings.ToLower(record.Text), term)
		}
		if match {
			filtered = append(filtered, record)
		}
	}
	if page.Offset > len(filtered) {
		return nil, "", companion.ErrMemoryConflict
	}
	startIndex := page.Offset
	stop := startIndex + min(limit, len(filtered)-startIndex)
	next := ""
	if stop < len(filtered) {
		page.Offset = stop
		raw, err := json.Marshal(page)
		if err != nil {
			return nil, "", err
		}
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return filtered[startIndex:stop], next, nil
}

func archivedMemoryPreview(record localWechatArchiveRecord) string {
	text := []rune(record.Text)
	return string(text[:min(160, len(text))])
}

func (t *localToolSet) searchWechatArchive(ctx context.Context, store *companion.Store, q localWechatMemoryPage, proof localWechatMemoryProof, requested int) (string, error) {
	records, err := t.wechat.wechatArchiveRecords(ctx, store, proof)
	if err != nil {
		return "", err
	}
	limit := min(requested, 50)
	page, cursor, err := archiveMemoryPage(records, proof, q.Query, q.Since, q.Before, "", q.Cursor, limit)
	if err != nil {
		return "", err
	}
	if t.wechatMemoryRefs == nil {
		t.wechatMemoryRefs = map[string]localWechatMemoryReference{}
		t.wechatMemoryPages = map[string]localWechatMemoryPage{}
	}
	items := []map[string]any{}
	for _, record := range page {
		token := "archive-reference-" + identity.RandomToken()
		t.wechatMemoryRefs[token] = localWechatMemoryReference{Proof: proof, ID: record.ID, Version: record.Version}
		items = append(items, map[string]any{"reference": token, "summary": archivedMemoryPreview(record), "kind": record.Kind, "revision": record.Version,
			"source": proof.Source, "record_origin": "legacy_import_snapshot", "original": record.Original})
	}
	next := ""
	if cursor != "" {
		next = "archive-cursor-" + identity.RandomToken()
		q.Proof, q.Cursor = proof, cursor
		t.wechatMemoryPages[next] = q
	}
	t.wechatMemoryProofs = append(t.wechatMemoryProofs, proof)
	if err := t.checkWechatMemoryResults(ctx); err != nil {
		return "", err
	}
	result, err := localMemoryJSON(map[string]any{"app_id": "wechat", "items": items, "next_cursor": next, "requested_limit": requested, "effective_limit": limit,
		"transport_page_limit": 50, "source": proof.Source, "data_role": "old_imported_memory_evidence"})
	if err != nil {
		return "", err
	}
	if err := t.auditWechatMemory(proof, true, len(items), result); err != nil {
		return "", err
	}
	return result, nil
}

func (t *localToolSet) readWechatArchiveReference(ctx context.Context, store *companion.Store, ref localWechatMemoryReference, in localWechatMemoryInput) (string, error) {
	records, err := t.wechat.wechatArchiveRecords(ctx, store, ref.Proof)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.ID == ref.ID && record.Version == ref.Version {
			value, err := archiveMemoryDetail(record, in.SourceIndex, localMemoryReadInput{ID: record.ID, Offset: in.Offset, Length: in.Length, SourceOffset: in.SourceOffset, SourceLimit: in.SourceLimit})
			if err != nil {
				return "", err
			}
			return localMemoryJSON(value)
		}
	}
	return "", companion.RecordUnavailable{}
}

func archiveMemoryDetail(record localWechatArchiveRecord, sourceIndex *int, input localMemoryReadInput) (map[string]any, error) {
	original := sourceIndex != nil
	if original {
		if *sourceIndex < 0 || *sourceIndex >= len(record.Sources) {
			return nil, apphost.ErrObservationInvalid
		}
		source := record.Sources[*sourceIndex]
		record.Text, record.At, record.Version = source.Text, source.OccurredAt, source.Revision
	}
	start, size, sourceOffset, sourceLimit, _, err := input.page()
	if err != nil {
		return nil, err
	}
	text, next, err := localMemoryPage(record.Text, start, size)
	if err != nil {
		return nil, err
	}
	sources := []map[string]any{}
	if sourceOffset > len(record.Sources) {
		return nil, apphost.ErrObservationInvalid
	}
	sourceEnd := sourceOffset + min(sourceLimit, len(record.Sources)-sourceOffset)
	for index := sourceOffset; index < sourceEnd; index++ {
		source := record.Sources[index]
		sources = append(sources, map[string]any{"source_index": index, "message_id": source.MessageID, "revision": source.Revision, "at": source.OccurredAt})
	}
	state := "old_derived_memory_not_live_current_fact"
	if original || record.Original {
		state = "old_original_message"
	}
	return map[string]any{"memory": palaceMemory{ID: record.ID, Title: archivedMemoryPreview(record), Kind: record.Kind, Status: "archived"},
		"detail": text, "version": record.Version, "sources": sources, "revisions": []any{}, "next_offset": next, "has_more": next < len(record.Text),
		"source_count": len(record.Sources), "next_source_offset": sourceEnd, "source_has_more": sourceEnd < len(record.Sources), "requested_source_limit": sourceLimit,
		"total_bytes": len(record.Text), "requested_length": size, "effective_length": next - start,
		"record_origin": "legacy_import_snapshot", "evidence_state": state, "original": original || record.Original}, nil
}
