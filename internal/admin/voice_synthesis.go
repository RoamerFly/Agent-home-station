package admin

import (
	"context"
	"encoding/json"
	"errors"
)

// Named synthesis is dispatched through the installed speech plugin. Resource
// paths and inference execution remain in the private, source-bound host broker.
// A named voice never falls through to the account's default provider voice.
func (p *permittedTools) synthesizeLibraryVoice(ctx context.Context, raw json.RawMessage) (string, error) {
	policy, err := p.newNamedSpeechPluginPolicy(ctx, raw)
	if err != nil {
		return "", err
	}
	_, err = policy.synthesize(ctx)
	if err != nil {
		return "", err
	}
	return policy.stage(ctx)
}

func (policy *namedSpeechPluginPolicy) stage(ctx context.Context) (string, error) {
	p := policy.p
	result := policy.checkpoint.Result
	data, err := policy.readAudio(ctx)
	if err != nil {
		return "", err
	}
	out, err := preparePublicAudioPreview(ctx, p.manager.dir, data, 0)
	if err != nil {
		return "", err
	}
	if err = policy.checkCompleted(ctx); err != nil {
		return "", err
	}
	voice := policy.resource.Voice
	staged, err := p.stageOutputWithAsset(withOutputAuthority(ctx, policy.checkCompleted), out, &assetOriginal{VoiceID: voice.ID, VoiceEngine: voice.Engine, VoiceComplete: true, Data: data, MIME: "audio/wav", Filename: voice.Name + "-语音.wav"})
	if err != nil {
		return "", err
	}
	var value map[string]any
	if json.Unmarshal([]byte(staged), &value) != nil {
		return "", errors.New("staged_voice_response_invalid")
	}
	value["voice_id"], value["voice_name"], value["engine"] = voice.ID, voice.Name, voice.Engine
	value["voice_resource_version"], value["device"], value["model_version"] = result.Voice.Version, result.Device, result.Version
	value["synthesized"], value["wechat_sends"] = true, 0
	value["voice_identity_evidence"] = "host_verified_registered_resources"
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
