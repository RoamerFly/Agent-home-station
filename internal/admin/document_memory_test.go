package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/documentmemory"
)

func TestDocumentMemorySurvivesNewTurnAndAssetReuse(t *testing.T) {
	f, p, n, a := artifactPipelineFixture(t)
	save := documentmemory.Input{Action: "save", Asset: a.ID, Summary: "已清空手写填写项，生成可编辑版本", Requirements: "保留印刷正文，不填写身份证"}
	raw, _ := json.Marshal(save)
	if _, err := p.documentMemory(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	// Reusing the same bytes must preserve processing notes.
	if _, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: []byte("synthetic immutable input"), MIME: "text/plain", Filename: "source.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	p.trace.TraceID = "later-request"
	raw, _ = json.Marshal(documentmemory.Input{Action: "read", Asset: a.ID})
	result, err := p.documentMemory(t.Context(), raw)
	if err != nil || !strings.Contains(result, "手写填写") {
		t.Fatal(result, err)
	}
	records, err := f.manager.loadAssetsSnapshot()
	if err != nil || len(records) == 0 || records[0].DocumentMemory == nil {
		t.Fatal("note was not persisted", err)
	}
	stale, _ := json.Marshal(save)
	if _, err = p.documentMemory(t.Context(), stale); err == nil {
		t.Fatal("stale revision overwritten")
	}
	search, _ := json.Marshal(documentmemory.Input{Action: "search", Query: "身份证", Limit: 1})
	if result, err = p.documentMemory(t.Context(), search); err != nil || !strings.Contains(result, a.ID) {
		t.Fatal(result, err)
	}
	p.trace.Thread = "another-room@chatroom"
	if _, err = p.documentMemory(t.Context(), raw); err == nil {
		t.Fatal("cross conversation note leaked")
	}
	if n.submits.Load() != 0 {
		t.Fatal("notes sent a message")
	}
}
