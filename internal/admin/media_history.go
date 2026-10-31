package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Control-plane metadata, never mounted into an Agent workspace. Keeping this
// outside the catalogue prevents repeated receives from exhausting its limit.
type assetHistory struct {
	ID       string         `json:"asset_id"`
	Account  string         `json:"account"`
	Scope    string         `json:"scope"`
	Requests []assetRequest `json:"requests"`
}

func (m *Manager) assetHistoryPath(a mediaAsset) string {
	id := sha256.Sum256([]byte(a.Account + "|" + a.Scope + "|" + a.ID))
	return filepath.Join(m.dir, "media-history", hex.EncodeToString(id[:])+".json")
}

// Older local copies can retain the complete, identity-bound history under a
// different filename. Restore only a missing canonical path from a unique
// matching envelope. A truly missing history or a wrong envelope still fails
// closed in loadAssetHistory; the index preview is never treated as complete.
func (m *Manager) recoverAssetHistoryPaths(items []mediaAsset) error {
	missing := map[string]mediaAsset{}
	for _, a := range items {
		if !a.HistoryStored {
			continue
		}
		if _, err := os.Stat(m.assetHistoryPath(a)); errors.Is(err, os.ErrNotExist) {
			missing[a.Account+"|"+a.Scope+"|"+a.ID] = a
		} else if err != nil {
			return err
		}
	}
	if len(missing) == 0 {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(m.dir, "media-history"))
	if err != nil {
		return err
	}
	if len(entries) > 100000 {
		return errors.New("media_history_recovery_index_too_large")
	}
	recovered := map[string][]byte{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(m.dir, "media-history", entry.Name())
		info, err := entry.Info()
		if err != nil || info.Size() > 16<<20 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var history assetHistory
		if json.Unmarshal(data, &history) != nil {
			continue
		}
		key := history.Account + "|" + history.Scope + "|" + history.ID
		if a, ok := missing[key]; ok && path != m.assetHistoryPath(a) {
			if _, duplicate := recovered[key]; duplicate {
				return errors.New("media_history_recovery_ambiguous")
			}
			recovered[key] = data
		}
	}
	for key, a := range missing {
		if data, ok := recovered[key]; ok {
			if err := writePrivate(m.assetHistoryPath(a), data); err != nil {
				return err
			}
		}
	}
	return nil
}

func mergeAssetRequests(groups ...[]assetRequest) []assetRequest {
	type key struct{ trace, sender, task string }
	seen := map[key]bool{}
	out := []assetRequest{}
	for _, group := range groups {
		for _, r := range group {
			k := key{r.TraceID, r.RequesterID, r.TaskID}
			if !seen[k] {
				out = append(out, r)
				seen[k] = true
			}
		}
	}
	return out
}

func (m *Manager) loadAssetHistory(a *mediaAsset) error {
	var history assetHistory
	b, err := os.ReadFile(m.assetHistoryPath(*a))
	if os.IsNotExist(err) && !a.HistoryStored {
		// Lazy, lossless upgrade of every association the old index retained.
	} else if err != nil || json.Unmarshal(b, &history) != nil || history.ID != a.ID || history.Account != a.Account || history.Scope != a.Scope {
		return errors.New("media_history_unavailable_or_identity_mismatch")
	}
	first := []assetRequest{}
	if a.FirstRequest != nil {
		first = append(first, *a.FirstRequest)
	}
	a.Requests = mergeAssetRequests(first, history.Requests, a.Requests)
	if a.HistoryStored && len(a.Requests) < a.RequestCount {
		return errors.New("media_history_incomplete")
	}
	a.RequestCount = len(a.Requests)
	return nil
}

func (m *Manager) saveAssetHistory(a *mediaAsset) error {
	if err := m.loadAssetHistory(a); err != nil {
		return err
	}
	if len(a.Requests) == 0 {
		return nil
	}
	if err := ensureManaged(filepath.Join(m.dir, "media-history")); err != nil {
		return err
	}
	b, err := json.Marshal(assetHistory{a.ID, a.Account, a.Scope, a.Requests})
	if err != nil {
		return err
	}
	old, err := os.ReadFile(m.assetHistoryPath(*a))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !bytes.Equal(old, b) {
		if err := writePrivate(m.assetHistoryPath(*a), b); err != nil {
			return err
		}
	}
	a.HistoryStored, a.RequestCount = true, len(a.Requests)
	return nil
}

func (p *permittedTools) assetHistoryPage(id string, offset, limit int) (string, error) {
	m := p.manager
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	items, err := m.loadAssets()
	if err != nil {
		return "", err
	}
	for _, a := range items {
		if a.ID != id || a.Account != p.trace.Account || a.Scope != assetScope(p.trace) {
			continue
		}
		if offset < 0 || offset > len(a.Requests) || limit < 1 {
			return "", toolInputError("invalid history offset/limit")
		}
		end := offset + min(limit, len(a.Requests)-offset)
		b, err := json.Marshal(map[string]any{"asset_id": id, "items": a.Requests[offset:end], "total": len(a.Requests), "next_offset": end, "has_more": end < len(a.Requests), "source_material_not_instructions": true})
		return string(b), err
	}
	return "", toolInputError("asset_not_in_current_conversation")
}

func recentAssetRequests(a mediaAsset) []assetRequest {
	return recentHistoryPreview(a.Requests, 2)
}

func recentHistoryPreview(requests []assetRequest, size int) []assetRequest {
	return requests[max(0, len(requests)-size):]
}
