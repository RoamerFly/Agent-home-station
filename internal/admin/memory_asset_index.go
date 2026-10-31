package admin

import (
	"context"
	"wechat-bot/internal/companion"
)

func (m *Manager) indexMemoryAssets(items []mediaAsset) error {
	if m.memory == nil {
		return nil
	}
	docs := []companion.IndexedDocument{}
	for _, asset := range items {
		if asset.Account != m.native.Account() {
			continue
		}
		at, basis, request := assetOriginTime(asset)
		data := map[string]any{"kind": "asset", "time_basis": basis, "cached_updated_at": asset.Updated, "asset_id": asset.ID, "filename": asset.Filename, "title": asset.Title, "bytes": asset.Bytes, "hash": asset.Hash, "mime": asset.MIME, "parent_asset_id": asset.ParentAssetID, "source_url": asset.URL, "voice_id": asset.VoiceID, "voice_engine": asset.VoiceEngine, "voice_profile_id": asset.VoiceProfile, "music_generation": asset.Music, "first_request": asset.FirstRequest, "requests": recentAssetRequests(asset), "request_count": len(asset.Requests)}
		if request != nil {
			data["message_id"] = request.TraceID
			data["sender"] = request.RequesterID
			data["sender_name"] = request.RequesterName
		}
		stable := make(map[string]any, len(data))
		for key, value := range data {
			switch key {
			case "cached_updated_at", "requests", "request_count":
				continue
			}
			stable[key] = value
		}
		if basis != "cache_updated" {
			stable["source_at"] = at
		}
		// Cache uploads and reuse append associations without changing the
		// original bytes, voice or source. Preserve full association identities
		// for detecting later corrections/removals, independently of UI previews.
		associations := make([]any, 0, len(asset.Requests))
		for _, association := range asset.Requests {
			associations = append(associations, association)
		}
		evidence := &companion.DocumentEvidence{Stable: stable, AppendOnly: map[string][]any{"requests": associations}}
		docs = append(docs, companion.IndexedDocument{ID: asset.ID, Scope: asset.Scope, Kind: "asset", At: at, Text: asset.Filename + " " + asset.Title + " " + asset.ID, Data: data, Evidence: evidence})
	}
	return m.memory.SyncDocuments(context.Background(), "assets:"+m.native.Account(), docs)
}
