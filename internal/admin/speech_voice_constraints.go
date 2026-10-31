package admin

import (
	"encoding/hex"
	"regexp"
	"strings"
)

// Library names are evidence for a missing explicit selection, never a reason
// to pick weights automatically. The actual identity is always a voice ID.
func validateRequestedSpeechVoice(request, voiceID string, voices []voiceEntry) error {
	matched := false
	selected := false
	for _, v := range voices {
		if len([]rune(v.Name)) < 2 {
			continue
		}
		if regexp.MustCompile(`(?i)(^|[^a-z0-9_])` + regexp.QuoteMeta(v.Name) + `($|[^a-z0-9_])`).MatchString(request) {
			matched = true
			selected = selected || v.ID == voiceID && voiceID != ""
		}
	}
	if matched && !selected {
		return &RuntimeFailure{Code: "named_voice_selection_required", Stage: "selection", Message: "请求指定了登记声线，需传匹配的voice_id；未提交默认声线替代", NotStarted: true, Continuation: "先voice_library_list/read确认对应说话声线，再用speech_synthesize传入voice_id"}
	}
	return nil
}

func validateMusicVoiceEvidence(status map[string]any, spec *songSpec) error {
	if spec == nil || spec.Engine != "rvc" || spec.VoiceProfile == "" {
		return nil
	}
	recipe, _ := status["recipe"].(map[string]any)
	voice, _ := status["singing_voice"].(map[string]any)
	engine, _ := voice["engine"].(string)
	profile, _ := voice["profile"].(string)
	hash, _ := voice["config_sha256"].(string)
	decoded, err := hex.DecodeString(hash)
	if recipe["voice_profile_id"] != spec.VoiceProfile || !strings.EqualFold(engine, "rvc") || profile == "" || err != nil || len(decoded) != 32 {
		return toolInputError("named_voice_result_identity_unverified: 已完成任务没有匹配的声线配方与模型配置验证证据，未登记或发送该音频")
	}
	if spec.ReferenceHash != "" && recipe["reference_source_sha256"] != spec.ReferenceHash {
		return toolInputError("named_voice_result_source_mismatch: 原录音配方与任务不一致，未发送")
	}
	return nil
}
