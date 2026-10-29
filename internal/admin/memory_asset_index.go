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
		data := map[string]any{"kind": "asset", "time_basis": basis, "cached_updated_at": asset.Updated, "asset_id": asset.ID, "filename": asset.Filename, "title": asset.Title, "bytes": asset.Bytes, "mime": asset.MIME, "parent_asset_id": asset.ParentAssetID, "first_request": asset.FirstRequest, "requests": recentAssetRequests(asset), "request_count": len(asset.Requests)}
		if request != nil {
			data["message_id"] = request.TraceID
			data["sender"] = request.RequesterID
			data["sender_name"] = request.RequesterName
		}
		docs = append(docs, companion.IndexedDocument{ID: asset.ID, Scope: asset.Scope, Kind: "asset", At: at, Text: asset.Filename + " " + asset.Title + " " + asset.ID, Data: data})
	}
	return m.memory.SyncDocuments(context.Background(), "assets:"+m.native.Account(), docs)
}
