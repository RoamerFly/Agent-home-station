package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestWorkspaceAndAssetToolsExposeLaterPages(t *testing.T) {
	f, p := trustedAgent(t)
	defer p.hub.Close()
	ctx := context.Background()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	for i := 0; i < 35; i++ {
		if _, e := f.manager.workspace.Write("shared", fmt.Sprintf("fixture-%02d.md", i), "synthetic searchable document", ""); e != nil {
			t.Fatal(e)
		}
	}
	value, e := p.Call(ctx, "workspace_search", json.RawMessage(`{"shared":true,"query":"synthetic searchable","offset":30,"limit":10}`))
	var docs struct {
		Documents []map[string]any
		Total     int
		Next      int  `json:"next_offset"`
		More      bool `json:"has_more"`
	}
	if e != nil || json.Unmarshal([]byte(value), &docs) != nil || len(docs.Documents) != 5 || docs.Total != 35 || docs.More || docs.Next != 35 {
		t.Fatal(value, e)
	}
	for i := 0; i < 14; i++ {
		if _, e := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: []byte(fmt.Sprintf("synthetic audio bytes%d", i)), MIME: "audio/wav", Filename: fmt.Sprintf("song%d.wav", i)}, nil); e != nil {
			t.Fatal(e)
		}
	}
	value, e = p.Call(ctx, "media_recent", json.RawMessage(`{"offset":12,"limit":10}`))
	var assets struct {
		Items []map[string]any
		Total int
		More  bool `json:"has_more"`
	}
	if e != nil || json.Unmarshal([]byte(value), &assets) != nil || len(assets.Items) != 2 || assets.Total != 14 || assets.More {
		t.Fatal(value, e)
	}
}
