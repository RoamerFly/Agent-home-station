package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"wechat-bot/internal/documentmemory"
	"wechat-bot/internal/hostfile"
)

func (t *localToolSet) documentMemory(ctx context.Context, raw json.RawMessage) (string, error) {
	var in documentmemory.Input
	if json.Unmarshal(raw, &in) != nil || t.access == nil || t.run == nil || !t.run.Tracked {
		return "", errors.New("文档档案需要当前任务授权")
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	store := t.access.home.artifacts
	store.noteMu.Lock()
	defer store.noteMu.Unlock()
	read := func(id string) (localArtifact, error) {
		a, e := store.read(id)
		if e == nil && (a.Fish != grant.FishID || a.Owner != grant.OwnerID) {
			e = errors.New("文档不属于当前主人")
		}
		return a, e
	}
	if in.Action == "search" {
		entries, err := os.ReadDir(filepath.Join(store.home.dataDir, "artifacts"))
		if err != nil {
			return "", err
		}
		rows := []map[string]any{}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			a, e := read(entry.Name())
			if e == nil && documentmemory.Match(a.DocumentMemory, a.Name, in.Query) {
				rows = append(rows, localDocumentMemoryView(a))
			}
		}
		page, err := documentmemory.Page(rows, in)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(page)
		return string(b), nil
	}
	a, err := read(in.Asset)
	if err != nil {
		return "", err
	}
	if in.Action == "read" {
		b, _ := json.Marshal(localDocumentMemoryView(a))
		return string(b), nil
	}
	if in.Action != "save" {
		return "", errors.New("未知文档档案操作")
	}
	for _, id := range in.Parents {
		if id == in.Asset {
			return "", errors.New("文档不能引用自身为前版")
		}
		if _, e := read(id); e != nil {
			return "", e
		}
	}
	note, err := in.Update(a.DocumentMemory, t.traceID)
	if err != nil {
		return "", err
	}
	if !t.access.current(grant) || ctx.Err() != nil {
		return "", errors.New("文档档案授权已变化")
	}
	a.DocumentMemory = note
	b, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	if err = hostfile.WritePrivate(filepath.Join(store.home.dataDir, "artifacts", a.ID, "record.json"), b); err != nil {
		return "", err
	}
	out, _ := json.Marshal(localDocumentMemoryView(a))
	return string(out), nil
}
func localDocumentMemoryView(a localArtifact) map[string]any {
	return map[string]any{"artifact_id": a.ID, "filename": a.Name, "sha256": a.SHA256, "source": a.Source, "parent": a.Parent, "document_note": a.DocumentMemory, "user_acceptance": "not_inferred"}
}
