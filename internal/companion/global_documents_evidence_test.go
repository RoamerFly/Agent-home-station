package companion

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestDocumentEvidenceCacheUpdateRefreshesPageWithoutInvalidating(t *testing.T) {
	s, _, in := memoryFixture(t)
	id := ScopeID(in.Channel, in.Account, in.Thread)
	doc := IndexedDocument{ID: "synthetic-evidence-doc", Scope: id, Kind: "asset", At: time.Now().UTC(), Text: "合成音频", Data: map[string]any{"asset_id": "synthetic-evidence-doc", "cache_at": "old", "request_count": 1}, Evidence: &DocumentEvidence{Stable: map[string]any{"hash": "content-hash", "voice_id": "named"}, AppendOnly: map[string][]any{"requests": {map[string]any{"message": "first", "sender": "alice"}}}}}
	if err := s.SyncDocuments(t.Context(), "safe-assets", []IndexedDocument{doc}); err != nil {
		t.Fatal(err)
	}
	recall := recallEpochForTest(t, s)
	data, err := s.DataEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	doc.Data["cache_at"], doc.Data["request_count"] = "new", 2
	doc.Evidence.AppendOnly["requests"] = append(doc.Evidence.AppendOnly["requests"], map[string]any{"message": "second", "sender": "bob"})
	if err = s.SyncDocuments(t.Context(), "safe-assets", []IndexedDocument{doc}); err != nil {
		t.Fatal(err)
	}
	if recallEpochForTest(t, s) != recall {
		t.Fatal("cache refresh or append invalidated old evidence")
	}
	if got, e := s.DataEpoch(t.Context()); e != nil || got == data {
		t.Fatal("changed payload retained old page epoch", got, e)
	}
	page, err := s.SearchGlobal(t.Context(), GlobalQuery{Labels: map[string]string{id: "safe"}})
	if err != nil || len(page.Items) != 1 || page.Items[0]["cache_at"] != "new" || page.Items[0]["request_count"] != float64(2) {
		t.Fatal("global page did not show current metadata", page, err)
	}
	dir := filepath.Dir(s.path)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.SyncDocuments(t.Context(), "safe-assets", []IndexedDocument{doc}); err != nil || recallEpochForTest(t, reopened) != recall {
		t.Fatal("durable evidence baseline lost at restart", err)
	}
}

func TestDocumentEvidenceProtectsSemanticCorrectionsAndAssociationRemoval(t *testing.T) {
	for _, mutation := range []string{"content", "scope", "kind", "text", "source", "voice", "association_role", "association_delete", "delete_document", "default_data"} {
		t.Run(mutation, func(t *testing.T) {
			s, _, in := memoryFixture(t)
			doc := IndexedDocument{ID: "evidence", Scope: ScopeID(in.Channel, in.Account, in.Thread), Kind: "asset", Text: "original title", At: time.Now(), Data: map[string]any{"fact": "original"}, Evidence: &DocumentEvidence{Stable: map[string]any{"hash": "original", "source": "source-event-1", "voice": "mom"}, AppendOnly: map[string][]any{"requests": {map[string]any{"message": "first", "sender": "alice"}, map[string]any{"message": "second", "sender": "bob"}}}}}
			if mutation == "default_data" {
				doc.Evidence = nil
			}
			if err := s.SyncDocuments(t.Context(), "safe", []IndexedDocument{doc}); err != nil {
				t.Fatal(err)
			}
			before := recallEpochForTest(t, s)
			switch mutation {
			case "content":
				doc.Evidence.Stable["hash"] = "changed-bytes"
			case "scope":
				doc.Scope = ScopeID("wechat", "other-account", "other-room")
			case "kind":
				doc.Kind = "message"
			case "text":
				doc.Text = "corrected title"
			case "source", "voice":
				doc.Evidence.Stable[mutation] = "corrected"
			case "association_role":
				doc.Evidence.AppendOnly["requests"][1] = map[string]any{"message": "second", "sender": "mallory"}
			case "association_delete":
				doc.Evidence.AppendOnly["requests"] = doc.Evidence.AppendOnly["requests"][:1]
			case "default_data":
				doc.Data["fact"] = "corrected"
			}
			docs := []IndexedDocument{doc}
			if mutation == "delete_document" {
				docs = nil
			}
			if err := s.SyncDocuments(t.Context(), "safe", docs); err != nil {
				t.Fatal(err)
			}
			if recallEpochForTest(t, s) == before {
				t.Fatal("semantic evidence change did not invalidate", mutation)
			}
		})
	}
}

func TestDocumentEvidenceLegacyBaselineAndFailedSnapshotAreAtomic(t *testing.T) {
	s, _, in := memoryFixture(t)
	doc := IndexedDocument{ID: "legacy-safe-doc", Scope: ScopeID(in.Channel, in.Account, in.Thread), Kind: "asset", Text: "fixture", Data: map[string]any{"cache_at": "old"}}
	if err := s.SyncDocuments(t.Context(), "safe", []IndexedDocument{doc}); err != nil {
		t.Fatal(err)
	}
	// Reproduce an older binary's projection without the new baseline metadata.
	if _, err := s.db.Exec("DELETE FROM meta WHERE key GLOB 'document_evidence:*'"); err != nil {
		t.Fatal(err)
	}
	before := recallEpochForTest(t, s)
	doc.Evidence = &DocumentEvidence{Stable: map[string]any{"hash": "bytes"}}
	if err := s.SyncDocuments(t.Context(), "safe", []IndexedDocument{doc}); err != nil {
		t.Fatal(err)
	}
	if recallEpochForTest(t, s) == before {
		t.Fatal("unverified legacy evidence policy accepted without invalidation")
	}
	before = recallEpochForTest(t, s)
	dataBefore, _ := s.DataEpoch(t.Context())
	doc.Evidence.Stable["bad"] = make(chan int)
	doc.Data["cache_at"] = "must roll back"
	if err := s.SyncDocuments(t.Context(), "safe", []IndexedDocument{doc}); err == nil {
		t.Fatal("unserializable evidence accepted")
	}
	var raw []byte
	if err := s.db.QueryRow("SELECT payload FROM search_documents WHERE namespace='safe' AND id=?", doc.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload["cache_at"] != "old" {
		t.Fatal("failed transaction changed payload", payload, err)
	}
	if got, _ := s.DataEpoch(t.Context()); got != dataBefore || recallEpochForTest(t, s) != before {
		t.Fatal("failed transaction published an epoch")
	}
}
