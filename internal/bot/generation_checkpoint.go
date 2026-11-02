package bot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

func generationToolCallKey(name string, args json.RawMessage) string {
	// Tool arguments are JSON objects.  Canonicalise valid JSON before deriving
	// the checkpoint key so a resumed model cannot accidentally repeat a
	// completed call merely by changing whitespace or object-key order.
	canonical := []byte(args)
	var decoded any
	if json.Unmarshal(args, &decoded) == nil {
		if encoded, err := json.Marshal(decoded); err == nil {
			canonical = encoded
		}
	}
	sum := sha256.Sum256(append(append([]byte(name+"\x00"), canonical...), 0))
	return hex.EncodeToString(sum[:])
}

func checkpointPreview(result string) string {
	r := []rune(strings.TrimSpace(result))
	if len(r) > 6000 {
		r = r[:6000]
	}
	return string(r)
}

func recoveryReadOnlyTool(name string) bool {
	if strings.HasPrefix(name, "memory_") && name != "memory_remember" && name != "memory_update" {
		return true
	}
	switch name {
	case "agent_global_search", "tool_output_read", "incoming_media_read", "video_inspect", "web_search", "web_fetch", "plugin_job_status", "music_local_status":
		return true
	}
	return false
}

func onlyReadOnlyCheckpoints(results []companion.RecoveryToolResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if !result.ReadOnly {
			return false
		}
	}
	return true
}

// recoveryPrivacySupported deliberately checks capability rather than current
// authorization. Authorization is evaluated by the host only after Engine has
// applied SetTrace to the resumed input. Without that host hook, a saved
// cross-conversation result must never be injected into a model prompt.
func recoveryPrivacySupported(tools companion.ToolAccess, in companion.Input) bool {
	if in.RecoveryPrivacy == nil || !in.RecoveryPrivacy.Required {
		return true
	}
	_, ok := tools.(interface{ RecoveryRouteValid() error })
	return ok
}

func (w *Worker) restoreGenerationCheckpoint(in companion.Input) companion.Input {
	if w.runtime == nil || in.TraceID == "" {
		return in
	}
	for _, item := range w.runtime.Inbox {
		if item.Event.Data.MessageID != in.TraceID || item.Phase != "retrying_model" {
			continue
		}
		in.RecoveryToolResults = append([]companion.RecoveryToolResult(nil), item.RecoveryTools...)
		if item.RecoveryEvidence != nil {
			copy := *item.RecoveryEvidence
			in.RecoveryEvidence = &copy
		}
		if item.RecoveryPrivacy != nil {
			copy := *item.RecoveryPrivacy
			copy.SourceThreads = append([]string(nil), item.RecoveryPrivacy.SourceThreads...)
			in.RecoveryPrivacy = &copy
			// A host-specific adapter must re-authorize this binding after SetTrace
			// and before Engine builds its model payload. The generic fallback is
			// fail-closed in start/respond generation setup.
		}
		return in
	}
	return in
}

func (w *Worker) appendGenerationCheckpoint(j *generationJob, name string, args json.RawMessage, result, resultID string, evidence companion.GenerationEvidence, hasEvidence bool, privacy ...*companion.RecoveryPrivacyBinding) error {
	if j == nil || result == "" {
		return nil
	}
	key := generationToolCallKey(name, args)
	for _, old := range j.input.RecoveryToolResults {
		if old.CallKey == key {
			return nil
		}
	}
	entry := companion.RecoveryToolResult{Tool: name, CallKey: key, Result: checkpointPreview(result), ResultID: resultID, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), ReadOnly: recoveryReadOnlyTool(name)}
	j.input.RecoveryToolResults = append(j.input.RecoveryToolResults, entry)
	if hasEvidence {
		copy := evidence
		j.input.RecoveryEvidence = &copy
	}
	if len(privacy) > 0 && privacy[0] != nil {
		copy := *privacy[0]
		copy.SourceThreads = append([]string(nil), privacy[0].SourceThreads...)
		j.input.RecoveryPrivacy = &copy
	}
	if w.runtime != nil {
		for i := range w.runtime.Inbox {
			item := &w.runtime.Inbox[i]
			if item.Event.Data.MessageID != j.input.TraceID {
				continue
			}
			item.RecoveryTools = append([]companion.RecoveryToolResult(nil), j.input.RecoveryToolResults...)
			if j.input.RecoveryEvidence != nil {
				copy := *j.input.RecoveryEvidence
				item.RecoveryEvidence = &copy
			}
			if j.input.RecoveryPrivacy != nil {
				copy := *j.input.RecoveryPrivacy
				copy.SourceThreads = append([]string(nil), j.input.RecoveryPrivacy.SourceThreads...)
				item.RecoveryPrivacy = &copy
			}
			break
		}
	}
	return w.saveRuntime()
}
