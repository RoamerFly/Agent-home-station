package station

import (
	"context"
	"errors"
	"os"
	"slices"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/workspacefs"
)

func (t *stationAttachmentTools) crossArchiveSources(worker *stationWechatWorker, threads []string) ([]string, error) {
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	sources := []string{}
	for _, thread := range threads {
		info, err := root.Lstat(stationLegacyMemoryFile(t.trace.Account, thread))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, apphost.ErrUnavailable
		}
		if info.Mode().IsRegular() {
			sources = append(sources, thread)
		}
	}
	return sources, nil
}

func (t *stationAttachmentTools) crossArchiveRecords(ctx context.Context, proof stationWechatCrossProof) ([]localWechatArchiveRecord, localWechatMemoryProof, error) {
	source := localWechatMemoryProof{
		Source:   localWechatMemorySource{Connection: t.crossConnection, Account: t.trace.Account, Thread: proof.Thread, Dataset: "archive"},
		Revision: proof.GrantRevision, Grant: proof.ArchiveGrant, Instance: proof.Instance, Epoch: proof.Epoch, Archive: proof.ArchiveFingerprint, ArchiveVisibility: proof.ArchiveVisibility,
	}
	records, err := t.crossHost.wechatArchiveRecords(ctx, t.store, source)
	return records, source, err
}

func (t *stationAttachmentTools) searchCrossArchive(ctx context.Context, query stationWechatCrossPage, proof stationWechatCrossProof, requested int) (string, error) {
	records, source, err := t.crossArchiveRecords(ctx, proof)
	if err != nil {
		return "", err
	}
	limit := min(requested, 50)
	page, cursor, err := archiveMemoryPage(records, source, query.Query, query.Since, query.Before, "", query.Cursor, limit)
	if err != nil {
		return "", err
	}
	items := []map[string]any{}
	for _, record := range page {
		token := "cross-archive-reference-" + identity.RandomToken()
		t.cross.Refs[token] = stationWechatCrossRef{Proof: proof, ID: record.ID, Version: record.Version}
		items = append(items, map[string]any{"reference": token, "summary": archivedMemoryPreview(record), "kind": record.Kind, "revision": record.Version,
			"source": "wechat", "thread_id": proof.Thread, "dataset": "archive", "record_origin": "legacy_import_snapshot", "original": record.Original})
	}
	next := ""
	if cursor != "" {
		next = "cross-archive-cursor-" + identity.RandomToken()
		query.Proof, query.Cursor = proof, cursor
		t.cross.Pages[next] = query
	}
	if !slices.Contains(t.cross.Proofs, proof) {
		t.cross.Proofs = append(t.cross.Proofs, proof)
	}
	result, err := localMemoryJSON(map[string]any{"items": items, "source": "wechat", "thread_id": proof.Thread, "dataset": "archive",
		"next_cursor": next, "requested_limit": requested, "effective_limit": limit, "transport_page_limit": 50,
		"data_role": "old_imported_memory_evidence", "delivery": "owner_private_only"})
	if err != nil {
		return "", err
	}
	return t.finishCrossMemory(ctx, proof, true, len(items), result)
}

func (t *stationAttachmentTools) readCrossArchive(ctx context.Context, ref stationWechatCrossRef, in stationWechatCrossInput) (string, error) {
	records, _, err := t.crossArchiveRecords(ctx, ref.Proof)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.ID != ref.ID || record.Version != ref.Version {
			continue
		}
		value, err := archiveMemoryDetail(record, in.SourceIndex, localMemoryReadInput{ID: record.ID, Offset: in.Offset, Length: in.Length,
			SourceOffset: in.SourceOffset, SourceLimit: in.SourceLimit})
		if err != nil {
			return "", err
		}
		value["source"], value["thread_id"], value["dataset"], value["delivery"] = "wechat", ref.Proof.Thread, "archive", "owner_private_only"
		result, err := localMemoryJSON(value)
		if err != nil {
			return "", err
		}
		return t.finishCrossMemory(ctx, ref.Proof, false, 1, result)
	}
	return "", companion.RecordUnavailable{}
}
