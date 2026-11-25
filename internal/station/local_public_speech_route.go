package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"wechat-bot/internal/websession"
)

const sharedSpeechRoute = "/api/local/project-tools/station-speech/synthesize"

// The legacy WeChat application uses the same workstation speech operation as
// local chat. The application keeps its own source, task and delivery records.
func (s *localProjectTools) sharedSpeech(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	record, err := s.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		websession.Fail(w, http.StatusConflict, "本机主人身份不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if r.Method == http.MethodGet {
		host, err := s.forFish(record.FishID)
		if err == nil {
			_, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localTTSPackage, capability: localTTSCapability})
		}
		if err != nil || s.tts == nil {
			websession.Fail(w, http.StatusServiceUnavailable, "本机语音工具未启用")
			return
		}
		info, err := s.tts.Info(ctx, record.FishID)
		if err != nil || info.Binding == "" || len(info.Voices) == 0 {
			websession.Fail(w, http.StatusServiceUnavailable, "没有可用的已登记声线")
			return
		}
		websession.Respond(w, http.StatusOK, map[string]any{"voices": info.Voices, "default_voice_id": info.DefaultVoiceID})
		return
	}
	var request localTTSRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		websession.Fail(w, http.StatusBadRequest, "语音合成参数无效")
		return
	}
	result, err := s.synthesizePublicSpeech(ctx, record.FishID, request)
	if err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	websession.Respond(w, http.StatusOK, map[string]any{
		"voice_id": result.VoiceID, "voice_version": result.Version,
		"speed": result.Speed, "wav_base64": base64.StdEncoding.EncodeToString(result.WAV),
	})
}
