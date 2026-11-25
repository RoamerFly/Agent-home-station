package companion

import (
	"encoding/json"
	"testing"
	"time"
)

func TestModelEchoedReplyProofIsRemintedFromVerifiedSource(t *testing.T) {
	store, config, in := memoryFixture(t)
	in.IsGroup = true
	in.TraceID = "model-proof-source"
	in.Text = "synthetic source"
	entry := sourceEntry(t, store, config, in, time.Now(), "fact", in.Text, 0)
	id := entry.Sources[0].EventID
	engine := &Engine{Store: store, Config: config}

	var d Decision
	modelJSON := `{"action":"reply","messages":["收到"],"reply_contexts":[{"target_ids":["` + in.Sender + `"],"source_event_ids":["` + id + `"],"context_version":"echoed-old-proof","state":"verified","source_versions":{"` + id + `":999},"evidence_epoch":"echoed-old-epoch"}]}`
	if err := json.Unmarshal([]byte(modelJSON), &d); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.normalizeReplyContexts(t.Context(), in, d); err == nil {
		t.Fatal("an echoed proof must not be accepted as host evidence")
	}
	clearModelReplyProof(&d)
	normalized, err := engine.normalizeReplyContexts(t.Context(), in, d)
	if err != nil {
		t.Fatal(err)
	}
	proof := normalized.ReplyContexts[0]
	if proof.SourceVersions[id] != 1 || proof.ContextVersion == "echoed-old-proof" || proof.ContextVersion == "" || proof.EvidenceEpoch != "" {
		t.Fatal("model proof was not replaced by the current source", proof)
	}
	if err := engine.ValidateReplyContext(t.Context(), in, proof); err != nil {
		t.Fatal(err)
	}
}
