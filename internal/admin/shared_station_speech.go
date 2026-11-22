package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const stationSpeechRoute = "/api/local/project-tools/station-speech/synthesize"

type stationSpeechVoice struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Version  string  `json:"version"`
	SpeedMin float64 `json:"speed_min"`
	SpeedMax float64 `json:"speed_max"`
}

type stationSpeechCatalog struct {
	Voices         []stationSpeechVoice `json:"voices"`
	DefaultVoiceID string               `json:"default_voice_id"`
}

func (p *permittedTools) stationSpeechCatalog(ctx context.Context) (stationSpeechCatalog, error) {
	var catalog stationSpeechCatalog
	if !p.privileged() {
		return catalog, errors.New("当前请求不是已核对的管理员会话")
	}
	session, err := p.stationToolLogin(ctx)
	if err != nil {
		return catalog, err
	}
	data, err := p.stationToolRequest(ctx, session, http.MethodGet, stationSpeechRoute, nil)
	if err != nil || json.Unmarshal(data, &catalog) != nil || len(catalog.Voices) == 0 {
		return stationSpeechCatalog{}, errors.New("工作站语音声线暂不可读")
	}
	return catalog, nil
}

func (c stationSpeechCatalog) voice(id string) (stationSpeechVoice, bool) {
	if id == "" {
		id = c.DefaultVoiceID
	}
	for _, voice := range c.Voices {
		if voice.ID == id && voice.Version != "" {
			return voice, true
		}
	}
	return stationSpeechVoice{}, false
}

func (p *permittedTools) synthesizeStationSpeech(ctx context.Context, raw json.RawMessage, catalog stationSpeechCatalog) (string, error) {
	var request struct {
		VoiceID  string   `json:"voice_id"`
		Text     string   `json:"text"`
		Speed    *float64 `json:"speed"`
		Language string   `json:"text_language"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &request) != nil || strings.TrimSpace(request.Text) == "" || len(request.Text) > 12000 {
		return "", toolInputError("station_speech_arguments_invalid")
	}
	for key := range fields {
		if key != "voice_id" && key != "text" && key != "speed" && key != "text_language" {
			return "", toolInputError("station_speech_unsupported_argument: " + key)
		}
	}
	voice, ok := catalog.voice(request.VoiceID)
	if !ok {
		return "", toolInputError("station_speech_voice_not_registered")
	}
	if request.Speed == nil {
		normal := 1.0
		request.Speed = &normal
	}
	if *request.Speed < voice.SpeedMin || *request.Speed > voice.SpeedMax {
		return "", toolInputError("station_speech_speed_out_of_range")
	}
	request.VoiceID = voice.ID
	session, err := p.stationToolLogin(ctx)
	if err != nil {
		return "", err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	data, err := p.stationToolRequest(bounded, session, http.MethodPost, stationSpeechRoute, request)
	if err != nil {
		return "", err
	}
	var result struct {
		VoiceID string `json:"voice_id"`
		Version string `json:"voice_version"`
		Audio   string `json:"wav_base64"`
	}
	if json.Unmarshal(data, &result) != nil || result.VoiceID != voice.ID || result.Version != voice.Version || len(result.Audio) > 44<<20 {
		return "", errors.New("工作站语音结果与所选声线不一致")
	}
	wav, err := base64.StdEncoding.DecodeString(result.Audio)
	if err != nil || len(wav) == 0 || len(wav) > 32<<20 {
		return "", errors.New("工作站语音音频无效")
	}
	if !p.privileged() {
		return "", errors.New("管理员会话已变化，未准备音频")
	}
	current, err := p.stationSpeechCatalog(ctx)
	if err != nil {
		return "", err
	}
	if now, ok := current.voice(voice.ID); !ok || now.Version != voice.Version {
		return "", errors.New("声线在合成期间已变更")
	}
	output, err := preparePublicAudioPreview(ctx, p.manager.dir, wav, 0)
	if err != nil {
		return "", err
	}
	staged, err := p.stageOutputWithAsset(ctx, output, &assetOriginal{VoiceID: voice.ID, VoiceEngine: "GPT-SoVITS", SynthesisText: request.Text, SynthesisTextKnown: true, VoiceComplete: true, Data: wav, MIME: "audio/wav", Filename: voice.Label + "-语音.wav"})
	if err != nil {
		return "", err
	}
	var value map[string]any
	if json.Unmarshal([]byte(staged), &value) != nil {
		return "", errors.New("工作站语音成品记录无效")
	}
	value["voice_id"], value["voice_name"], value["voice_resource_version"] = voice.ID, voice.Label, voice.Version
	value["engine"], value["synthesized"], value["wechat_sends"] = "GPT-SoVITS", true, 0
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
