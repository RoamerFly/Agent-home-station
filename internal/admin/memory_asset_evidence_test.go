package admin

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func syntheticIndexAsset() mediaAsset {
	stamp := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := assetRequest{MessageTime: stamp.Format(time.RFC3339Nano), TraceID: "original-source", RequesterID: "alice", RequesterName: "甲", Text: "请用指定声线讲解", At: stamp}
	return mediaAsset{ID: "synthetic-asset", Account: "test", Scope: companion.ScopeID("wechat", "test", "room"), Title: "合成语音", Filename: "speech.wav", MIME: "audio/wav", Hash: "safe-content-hash", Bytes: 100, URL: "https://example.invalid/original", VoiceID: "named-voice", VoiceEngine: "fixture", VoiceProfile: "named-profile", ParentAssetID: "original-video", FirstRequest: &first, Requests: []assetRequest{first}, Updated: stamp}
}

func assetEvidenceEpochs(t *testing.T, m *Manager) (string, string) {
	t.Helper()
	recall, err := m.memory.RecallEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data, err := m.memory.DataEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return recall, data
}

func TestAssetIndexCacheAndReuseAppendKeepEvidenceWhileUpdatingPage(t *testing.T) {
	f := setup(t)
	asset := syntheticIndexAsset()
	if err := f.manager.indexMemoryAssets([]mediaAsset{asset}); err != nil {
		t.Fatal(err)
	}
	before, data := assetEvidenceEpochs(t, f.manager)
	for i := 1; i <= 8; i++ {
		asset.Updated = asset.Updated.Add(time.Minute)
		asset.Requests = append(asset.Requests, assetRequest{TraceID: "reuse-" + time.Duration(i).String(), RequesterID: "bob", At: asset.Updated})
		asset.Sources = map[string]string{"voice": "refreshed-native-cache"}
		if err := f.manager.indexMemoryAssets([]mediaAsset{asset}); err != nil {
			t.Fatal(err)
		}
		if got, updated := assetEvidenceEpochs(t, f.manager); got != before || updated == data {
			t.Fatal("cache/reuse invalidated evidence or retained stale page", got, before, updated, data)
		}
		_, data = assetEvidenceEpochs(t, f.manager)
	}
	page, err := f.manager.memory.SearchGlobal(t.Context(), companion.GlobalQuery{Labels: map[string]string{asset.Scope: "合成群"}})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if page.Items[0]["request_count"] != float64(9) || page.Items[0]["voice_id"] != asset.VoiceID || page.Items[0]["hash"] != asset.Hash || len(page.Items[0]["requests"].([]any)) != 2 {
		t.Fatal("query-visible metadata not refreshed", page.Items)
	}
}

func TestAssetIndexSemanticCorrectionsInvalidateEvidence(t *testing.T) {
	for _, mutation := range []string{"hash_same_size", "bytes", "origin", "original_role", "later_role", "association_removed", "voice", "engine", "profile", "title", "scope", "parent", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			f := setup(t)
			asset := syntheticIndexAsset()
			asset.Requests = append(asset.Requests, assetRequest{TraceID: "second-source", RequesterID: "bob", At: asset.Updated.Add(time.Minute)})
			if err := f.manager.indexMemoryAssets([]mediaAsset{asset}); err != nil {
				t.Fatal(err)
			}
			before, _ := assetEvidenceEpochs(t, f.manager)
			switch mutation {
			case "hash_same_size":
				asset.Hash = "changed-content-hash"
			case "bytes":
				asset.Bytes++
			case "origin":
				asset.FirstRequest.TraceID = "corrected-original-source"
			case "original_role":
				asset.FirstRequest.RequesterID = "mallory"
			case "later_role":
				asset.Requests[1].RequesterID = "mallory"
			case "association_removed":
				asset.Requests = asset.Requests[:1]
			case "voice":
				asset.VoiceID = "different-voice"
			case "engine":
				asset.VoiceEngine = "different-engine"
			case "profile":
				asset.VoiceProfile = "different-profile"
			case "title":
				asset.Title = "corrected factual title"
			case "scope":
				asset.Scope = companion.ScopeID("wechat", "test", "other-room")
			case "parent":
				asset.ParentAssetID = "different-source-video"
			}
			items := []mediaAsset{asset}
			if mutation == "delete" {
				items = nil
			}
			if err := f.manager.indexMemoryAssets(items); err != nil {
				t.Fatal(err)
			}
			if got, _ := assetEvidenceEpochs(t, f.manager); got == before {
				t.Fatal("changed asset evidence remained valid", mutation)
			}
		})
	}
}

func TestAssetLegacyEvidenceBaselineIsEstablishedDuringManagerStartup(t *testing.T) {
	dir := t.TempDir()
	native := &fakeNative{}
	m, err := NewManager(dir, "", native)
	if err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "room", Sender: "alice", TraceID: "original", MessageTime: "2026-10-01T12:00:00Z", Text: "请讲解"}
	original := assetOriginal{Data: []byte("safe synthetic named voice bytes"), MIME: "audio/wav", Filename: "speech.wav", VoiceID: "named", VoiceEngine: "fixture", VoiceProfile: "named-profile"}
	if _, err = m.rememberAssetFormats(in, original, nil); err != nil {
		m.Close()
		t.Fatal(err)
	}
	before, _ := assetEvidenceEpochs(t, m)
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	// Synthetic pre-upgrade metadata: older binaries persisted only full hashes.
	db, err := sql.Open("sqlite", filepath.Join(dir, "companion", "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM meta WHERE key GLOB 'document_evidence:*'"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(dir, "", native)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	readyEpoch, _ := assetEvidenceEpochs(t, m)
	if readyEpoch == before {
		t.Fatal("manager became available before establishing conservative baseline")
	}
	in.TraceID = "reuse-after-startup"
	if _, err = m.rememberAssetFormats(in, original, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := assetEvidenceEpochs(t, m); got != readyEpoch {
		t.Fatal("first post-startup reuse unexpectedly invalidated new decisions", readyEpoch, got)
	}
}
