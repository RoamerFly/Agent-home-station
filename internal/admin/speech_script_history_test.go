package admin

import (
	"encoding/json"
	"testing"

	"wechat-bot/internal/companion"
)

func TestLegacySpeechScriptRequiresCompletedScopedEvidence(t *testing.T) {
	const account = "fixture"
	scope := companion.ScopeID("wechat", account, "chat")
	sources := map[string]map[string]companion.MediaMemory{scope: {"asset": {RequestTraceID: "turn"}}}
	op := func(thread, state, script string) agentOperation {
		args, _ := json.Marshal(map[string]string{"text": script})
		id := agentID(account, thread, "turn", "speech_synthesize", string(args))
		return agentOperation{ID: id, Thread: thread, State: state, Tool: "speech_synthesize", Arguments: args,
			Result: `{"operation_id":"` + id + `","media":[{"kind":"voice","asset_id":"asset"}]}`}
	}
	ops := make(map[string]agentOperation)
	for _, row := range []agentOperation{op("chat", "completed", "linen"), op("chat", "needs_review", "cotton"), op("another-chat", "completed", "wool")} {
		ops[row.ID] = row
	}
	joinLegacySpeechScripts(account, ops, sources)
	if row := sources[scope]["asset"]; row.SynthesisText != "linen" || !row.SynthesisTextKnown || row.State != "" {
		t.Fatalf("incorrect legacy script or invented receipt: %+v", row)
	}
	sources[scope]["asset"] = companion.MediaMemory{RequestTraceID: "turn"}
	other := op("chat", "completed", "silk")
	ops[other.ID] = other
	joinLegacySpeechScripts(account, ops, sources)
	if sources[scope]["asset"].SynthesisTextKnown {
		t.Fatal("ambiguous legacy synthesis was attributed")
	}
}
