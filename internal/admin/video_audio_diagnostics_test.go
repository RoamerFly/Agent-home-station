package admin

import (
	"errors"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediaspec"
)

func TestVideoAudioErrorsDistinguishNoSpeechSourceAndBackend(t *testing.T) {
	noSpeech := &mediaspec.Failure{Code: "ASR_RESPONSE_HAVE_NO_WORDS"}
	if !mediaspec.IsNoSpeech(videoAudioTranscriptionError(noSpeech)) {
		t.Fatal("no speech became service failure")
	}
	for _, tc := range []struct{ underlying, code string }{
		{"share_persisted_source_unavailable", "video_audio_source_unverified"},
		{"media_plugin_task_changed", "video_audio_plugin_result_unavailable"},
		{"local_asr_profile_unavailable", "video_audio_backend_unavailable"},
		{"private-token-and-path", "video_audio_transcription_failed"},
	} {
		err := videoAudioTranscriptionError(errors.New(tc.underlying))
		var safe interface{ SafeToolFailure() (string, string) }
		if !errors.As(err, &safe) {
			t.Fatal(err)
		}
		code, message := safe.SafeToolFailure()
		if code != tc.code || strings.Contains(message, "private-token") {
			t.Fatal(code, message)
		}
	}
	_, p := globalSearchFixture(t)
	for _, args := range []string{`{"id":"missing-original","event":true}`, `{"id":"missing-memory"}`} {
		_, err := p.Call(t.Context(), "memory_read", []byte(args))
		var safe memoryRecordUnavailable
		if !errors.As(err, &safe) || strings.Contains(mcptools.FailureDescription(err), "sql:") {
			t.Fatal("missing source leaked SQL instead of actionable diagnosis", err)
		}
	}
}
