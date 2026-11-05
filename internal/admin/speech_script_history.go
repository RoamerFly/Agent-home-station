package admin

import (
	"encoding/json"
	"strings"

	"wechat-bot/internal/companion"
)

// Older assets predate script metadata. Join only a locally completed TTS
// operation to an existing asset in that operation's original chat. A receipt
// for sending or playing the recording cannot be inferred from this ledger.
func joinLegacySpeechScripts(account string, operations map[string]agentOperation, sources map[string]map[string]companion.MediaMemory) {
	type identity struct{ scope, asset string }
	scripts := make(map[identity]string)
	for id, op := range operations {
		if id == "" || id != op.ID || op.Tool != "speech_synthesize" || op.State != "completed" || op.Thread == "" {
			continue
		}
		scope := companion.ScopeID("wechat", account, op.Thread)
		if len(sources[scope]) == 0 {
			continue
		}
		var args struct{ Text string }
		var result struct {
			OperationID string `json:"operation_id"`
			Media       []struct {
				Kind    string
				AssetID string `json:"asset_id"`
			}
		}
		if json.Unmarshal(op.Arguments, &args) != nil || strings.TrimSpace(args.Text) == "" ||
			json.Unmarshal([]byte(op.Result), &result) != nil || result.OperationID != id {
			continue
		}
		for _, item := range result.Media {
			source, exists := sources[scope][item.AssetID]
			if !exists || item.Kind != "voice" || item.AssetID == "" || source.RequestTraceID == "" ||
				agentID(account, op.Thread, source.RequestTraceID, op.Tool, string(op.Arguments)) != id {
				continue
			}
			key := identity{scope, item.AssetID}
			if old, seen := scripts[key]; seen && old != args.Text {
				scripts[key] = "" // one recording, conflicting scripts: retain unknown
			} else {
				scripts[key] = args.Text
			}
		}
	}
	for key, script := range scripts {
		row := sources[key.scope][key.asset]
		if script != "" && !row.SynthesisTextKnown {
			row.SynthesisText, row.SynthesisTextKnown = script, true
			sources[key.scope][key.asset] = row
		}
	}
}
