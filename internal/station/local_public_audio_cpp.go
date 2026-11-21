package station

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediaspec"
)

func (s *localProjectTools) publicAudioCppInfo(ctx context.Context, fish string) (localAudioCppDeployment, error) {
	if s == nil || s.audio == nil {
		return localAudioCppDeployment{}, errors.New("工作站 audio.cpp 后端不可用")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return localAudioCppDeployment{}, err
	}
	if _, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localAudioCppPackage, capability: "audio-cpp.models"}); err != nil {
		return localAudioCppDeployment{}, err
	}
	info, err := s.audio.Info(ctx, fish)
	if err != nil || info.Binding == "" {
		return localAudioCppDeployment{}, errors.New("工作站 audio.cpp 未登记")
	}
	return info, nil
}

func (s *localProjectTools) publicAudioCppSynthesize(ctx context.Context, fish, binding string, request localTTSRequest) (localTTSResult, error) {
	var empty localTTSResult
	host, err := s.forFish(fish)
	if err != nil {
		return empty, err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localAudioCppPackage, capability: "audio-cpp.synthesize"})
	if err != nil {
		return empty, err
	}
	configuration, err := host.store.Configuration(ctx, localAudioCppPackage)
	if err != nil {
		return empty, err
	}
	info, err := s.publicAudioCppInfo(ctx, fish)
	if err != nil || info.Binding != binding || binding == "" {
		return empty, errors.New("工作站 audio.cpp 登记已变化")
	}
	if !utf8.ValidString(request.Text) || strings.TrimSpace(request.Text) == "" || strings.ContainsRune(request.Text, 0) || len(request.Text) > localTTSTextLimit {
		return empty, mcptools.ArgumentError("$.text", "正文须为有效 UTF-8 且不超过 12000 字节")
	}
	if request.VoiceID != "" && request.VoiceID != "kokoro" {
		return empty, mcptools.ArgumentError("$.voice_id", "当前只支持登记的 Kokoro 模型")
	}
	request.VoiceID = "kokoro"
	result, err := s.audio.Synthesize(ctx, fish, binding, request)
	if err != nil {
		return empty, err
	}
	current, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localAudioCppPackage, capability: "audio-cpp.synthesize"})
	currentConfig, configErr := host.store.Configuration(ctx, localAudioCppPackage)
	currentInfo, infoErr := s.audio.Info(ctx, fish)
	if err != nil || configErr != nil || infoErr != nil || current.Revision != installed.Revision || currentConfig.Revision != configuration.Revision ||
		currentInfo.Binding != binding || ctx.Err() != nil {
		return empty, errors.New("audio.cpp 处理期间授权或登记已变化")
	}
	rate, seconds, err := mediaspec.PCM16WAVInfo(result.WAV)
	if err != nil || len(result.WAV) > localAudioCppMaxWAV || rate < 8000 || rate > 192000 || seconds <= 0 || result.VoiceID != "kokoro" || result.Version != binding {
		return empty, errors.New("audio.cpp 成品未通过 WAV 核验")
	}
	return result, nil
}

// Transcribe one already decoded PCM WAV page. The application owns the
// original attachment, its source check, range selection and continuation.
func (s *localProjectTools) publicAudioCppTranscribe(ctx context.Context, fish, binding string, wav []byte) (string, error) {
	host, err := s.forFish(fish)
	if err != nil {
		return "", err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localAudioCppPackage, capability: "audio-cpp.transcribe"})
	if err != nil {
		return "", err
	}
	configuration, err := host.store.Configuration(ctx, localAudioCppPackage)
	if err != nil {
		return "", err
	}
	info, err := s.publicAudioCppInfo(ctx, fish)
	if err != nil || info.Binding != binding || binding == "" {
		return "", errors.New("工作站 audio.cpp 登记已变化")
	}
	if len(wav) == 0 || len(wav) > localAudioCppMaxWAV {
		return "", errors.New("audio.cpp 输入 WAV 大小无效")
	}
	if _, _, err := mediaspec.PCM16WAVInfo(wav); err != nil {
		return "", errors.New("audio.cpp 输入不是 PCM16 WAV")
	}
	text, err := s.audio.Transcribe(ctx, fish, binding, wav)
	if err != nil {
		return "", err
	}
	current, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localAudioCppPackage, capability: "audio-cpp.transcribe"})
	currentConfig, configErr := host.store.Configuration(ctx, localAudioCppPackage)
	currentInfo, infoErr := s.audio.Info(ctx, fish)
	if err != nil || configErr != nil || infoErr != nil || current.Revision != installed.Revision || currentConfig.Revision != configuration.Revision ||
		currentInfo.Binding != binding || ctx.Err() != nil {
		return "", errors.New("audio.cpp 转写期间授权或登记已变化")
	}
	return text, nil
}
