package station

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediaspec"
)

// preparePublicSpeechRequest applies the workstation's registered voice and
// parameter rules. It has no knowledge of an application or message source.
func preparePublicSpeechRequest(info localTTSDeployment, request localTTSRequest) (localTTSRequest, localTTSVoice, error) {
	if !utf8.ValidString(request.Text) || strings.TrimSpace(request.Text) == "" || strings.ContainsRune(request.Text, 0) {
		return request, localTTSVoice{}, mcptools.ArgumentError("$.text", "需要有效的非空正文")
	}
	if len(request.Text) > localTTSTextLimit {
		return request, localTTSVoice{}, mcptools.ArgumentError("$.text", "单次合成最多 12000 UTF-8 字节，可以分段合成")
	}
	if request.Language != "" && !localTTSLanguageAllowed(request.Language) {
		return request, localTTSVoice{}, mcptools.ArgumentError("$.text_language", "语言模式不受支持，可查看工具声明或留空使用声线登记值")
	}
	if request.VoiceID == "" {
		request.VoiceID = info.DefaultVoiceID
	}
	var selected localTTSVoice
	for _, voice := range info.Voices {
		if voice.ID == request.VoiceID {
			selected = voice
			break
		}
	}
	if selected.Version == "" {
		return request, localTTSVoice{}, mcptools.ArgumentError("$.voice_id", "指定声线未登记或已撤权，请查看可用声线列表")
	}
	if request.Speed == nil {
		normal := 1.0
		request.Speed = &normal
	}
	if *request.Speed < selected.SpeedMin || *request.Speed > selected.SpeedMax {
		return request, localTTSVoice{}, mcptools.ArgumentError("$.speed", "语速超出该声线支持范围，请查看声线列表中的上下限")
	}
	return request, selected, nil
}

// synthesizePublicSpeech is the shared tool operation. The calling application
// owns admission, message handling and delivery; this operation only resolves
// the workstation registration, processes the request and verifies its output.
func (s *localProjectTools) synthesizePublicSpeech(ctx context.Context, fish string, request localTTSRequest) (localTTSResult, error) {
	if s == nil || s.tts == nil {
		return localTTSResult{}, errors.New("本机语音后端不可用")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return localTTSResult{}, err
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localTTSPackage, capability: localTTSCapability}); err != nil {
		return localTTSResult{}, err
	}
	info, err := s.tts.Info(ctx, fish)
	if err != nil || info.Binding == "" {
		return localTTSResult{}, errors.New("请在工作站核对本机语音后端和声线")
	}
	request, selected, err := preparePublicSpeechRequest(info, request)
	if err != nil {
		return localTTSResult{}, err
	}
	result, err := s.tts.Synthesize(ctx, fish, info.Binding, request)
	if err != nil {
		return localTTSResult{}, err
	}
	if result.VoiceID != selected.ID || result.Version != selected.Version || result.Speed != *request.Speed {
		return localTTSResult{}, errors.New("本机语音结果与请求声线不一致")
	}
	rate, _, err := mediaspec.PCM16WAVInfo(result.WAV)
	if err != nil || len(result.WAV) > localTTSWAVLimit || rate < 8000 || rate > 192000 {
		return localTTSResult{}, errors.New("本机语音结果不是有效 WAV")
	}
	return result, nil
}
