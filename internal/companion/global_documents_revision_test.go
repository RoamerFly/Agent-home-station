package companion

import (
	"testing"
	"time"
)

func TestDocumentCorrectionInvalidatesEvidenceWithoutTreatingAppendAsCorrection(t *testing.T) {
	s, _, in := memoryFixture(t)
	ctx := t.Context()
	doc := IndexedDocument{ID: "synthetic-document", Scope: ScopeID(in.Channel, in.Account, in.Thread), Kind: "asset", Text: "旧的合成说明", At: time.Now(), Data: map[string]any{"id": "synthetic-document"}}
	if err := s.SyncDocuments(ctx, "synthetic", []IndexedDocument{doc}); err != nil {
		t.Fatal(err)
	}
	first, _ := s.RecallEpoch(ctx)
	data, _ := s.DataEpoch(ctx)
	if err := s.SyncDocuments(ctx, "synthetic", []IndexedDocument{doc}); err != nil {
		t.Fatal(err)
	}
	if now, _ := s.RecallEpoch(ctx); now != first {
		t.Fatal("unchanged projection invalidated evidence")
	}
	if now, _ := s.DataEpoch(ctx); now != data {
		t.Fatal("unchanged projection invalidated pagination")
	}
	other := doc
	other.ID = "synthetic-new-document"
	other.Text = "新追加资料"
	if err := s.SyncDocuments(ctx, "synthetic", []IndexedDocument{doc, other}); err != nil {
		t.Fatal(err)
	}
	if now, _ := s.RecallEpoch(ctx); now != first {
		t.Fatal("independent append invalidated existing reply")
	}
	doc.Text = "修正后的合成说明"
	if err := s.SyncDocuments(ctx, "synthetic", []IndexedDocument{doc, other}); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.RecallEpoch(ctx)
	if updated == first {
		t.Fatal("text correction with unchanged Data did not invalidate old evidence")
	}
	var text string
	if err := s.db.QueryRowContext(ctx, "SELECT text FROM search_documents WHERE namespace=? AND id=?", "synthetic", doc.ID).Scan(&text); err != nil || text != doc.Text {
		t.Fatal("corrected body not searchable", text, err)
	}
	if err := s.SyncDocuments(ctx, "synthetic", []IndexedDocument{other}); err != nil {
		t.Fatal(err)
	}
	if now, _ := s.RecallEpoch(ctx); now == updated {
		t.Fatal("removed projection left old evidence valid")
	}
}
