package admin

import (
	"context"
	"encoding/json"
	"errors"

	"wechat-bot/internal/mcptools"
)

// rvc_target_id belongs to the host. Neither TTS provider receives it.
func splitRVCTarget(raw json.RawMessage) (json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, "", toolInputError("speech_arguments_must_be_an_object")
	}
	v, ok := fields["rvc_target_id"]
	if !ok {
		return raw, "", nil
	}
	var id string
	if json.Unmarshal(v, &id) != nil || !namedSpeechVoiceIDPattern.MatchString(id) {
		return nil, "", toolInputError("请选择声线库中登记的 RVC 目标声线")
	}
	delete(fields, "rvc_target_id")
	base, err := json.Marshal(fields)
	return base, id, err
}

func (p *permittedTools) checkSpeechBinding(sourceID, targetID string) error {
	if targetID == "" {
		return p.bindTaskSpeechVoice(sourceID)
	}
	selected, profile := p.SelectedVoiceIdentity()
	if selected != targetID || profile == "" {
		return toolInputError("换声目标已变更，未使用底稿")
	}
	return nil
}

// Use the same authorized, idempotent RVC submission and durable follow-up as
// a cached recording. Only the loader differs: no upload, catalogue entry or
// sendable attachment is created for the intermediate recording.
func (p *permittedTools) speakWithRVC(ctx context.Context, raw json.RawMessage, targetID string) (string, error) {
	if !p.enabled("music_rvc_start") || !p.manager.Config().Companion.Permissions.SendVoice {
		return "", toolInputError("朗读换声需要已授权的音乐换声工具和语音发送权限")
	}
	var in struct {
		VoiceID string `json:"voice_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", err
	}
	if in.VoiceID == "" && !p.manager.speechPluginConfigured(ctx) {
		return "", toolInputError("底稿需要一个可用的说话声线。保留 rvc_target_id，用 voice_id 选择库中的 GPT-SoVITS 声线")
	}
	p.privateDiagnostics = true
	request := rvcRequest{Title: "语音朗读", VoiceID: targetID, Kind: "voice"}
	return p.startRVCWithReference(ctx, request, func() (mediaAsset, []byte, error) {
		var audio []byte
		var job string
		var err error
		if in.VoiceID != "" {
			var policy *namedSpeechPluginPolicy
			policy, err = p.namedSpeechPolicy(ctx, raw, targetID)
			if err == nil {
				_, err = policy.synthesize(ctx)
			}
			if err == nil {
				audio, err = policy.readAudio(ctx)
				job = policy.completedJob.ID
			}
		} else {
			var policy *speechPluginPolicy
			policy, err = p.newSpeechPluginPolicy(ctx, raw)
			if err == nil {
				var result speechPluginResult
				result, err = policy.synthesize(ctx)
				if err == nil {
					audio, _, err = policy.readAudio(ctx, result.URL, 25<<20, mcptools.GetPublicMedia)
					job = policy.completedJob.ID
				}
			}
		}
		if err != nil {
			return mediaAsset{}, nil, err
		}
		if job == "" || len(audio) == 0 || len(audio) > 25<<20 {
			return mediaAsset{}, nil, errors.New("换声底稿未取得有效音频或超过 25 MiB")
		}
		if err := p.checkSpeechBinding(in.VoiceID, targetID); err != nil {
			return mediaAsset{}, nil, err
		}
		return mediaAsset{ID: "speech-job-" + job, Hash: namedSpeechSHA(audio)}, audio, nil
	})
}
