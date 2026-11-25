package station

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/strictjson"
	"wechat-bot/internal/websession"
)

const sharedASRRoute = "/api/local/project-tools/audio-cpp/transcribe"

// Connected application hosts verify the original source before submitting
// audio to this authenticated local service. No file or URL is opened here.
func (s *localProjectTools) sharedASR(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	version, err := s.home.localStationASRVersion(r.Context())
	if r.Method == http.MethodGet {
		websession.Respond(w, 200, map[string]any{"ready": err == nil && version != "", "version": version})
		return
	}
	if err != nil || version == "" {
		websession.Fail(w, 409, "station_asr_unavailable")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		websession.Fail(w, 415, "station_asr_json_required")
		return
	}
	var input struct {
		Audio   []byte `json:"audio"`
		Version string `json:"version"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 14<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil || strictjson.DecodeKnownFields(body, &input) != nil {
		websession.Fail(w, 400, "station_asr_invalid_request")
		return
	}
	audio := input.Audio
	if len(audio) < 44 || len(audio) > 10<<20 || string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		websession.Fail(w, 400, "station_asr_invalid_wav")
		return
	}
	check := func() error {
		current, err := s.home.localStationASRVersion(r.Context())
		if err != nil || current != version || input.Version != version {
			return errors.New("station_asr_binding_changed")
		}
		return nil
	}
	text, err := s.home.transcribeLocalStationAudio(r.Context(), audio, check)
	switch {
	case mediaspec.IsNoSpeech(err):
		websession.Respond(w, 200, map[string]string{"status": "no_recognizable_speech"})
	case err != nil:
		websession.Fail(w, 409, "station_asr_transcription_failed")
	default:
		websession.Respond(w, 200, map[string]string{"status": "transcribed", "text": text})
	}
}
