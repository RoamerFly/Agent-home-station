package admin

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestNamedSpeechRequestDiagnosesUnsupportedAndInvalidArgumentsBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "cloud_instruction_is_not_a_named_voice_option",
			raw:  `{"voice_id":"voice-a","text":"hello","instruction":"slow"}`,
			want: "named_speech_unsupported_argument: instruction",
		},
		{
			name: "unknown_field_is_not_reported_as_runtime_failure",
			raw:  `{"voice_id":"voice-a","text":"hello","unexpected":true}`,
			want: "named_speech_unsupported_argument: unexpected",
		},
		{
			name: "parameters_must_remain_an_object",
			raw:  `{"voice_id":"voice-a","text":"hello","parameters":["slow"]}`,
			want: "named_speech_parameters_must_be_an_object",
		},
		{
			name: "explicit_zero_timeout_is_not_silently_defaulted",
			raw:  `{"voice_id":"voice-a","text":"hello","timeout_seconds":0}`,
			want: "named_speech_timeout_seconds_must_be_positive",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeNamedSpeechRequest(json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestNamedSpeechRequestDefaultsAndSupportsDocumentedSpeedFactor(t *testing.T) {
	in, err := decodeNamedSpeechRequest(json.RawMessage(`{"voice_id":"voice-a","text":"慢一点","parameters":{"speed_factor":0.8,"sample_steps":64}}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Device != "cuda" || in.Version != "v2" || in.Timeout != 600 {
		t.Fatalf("named defaults lost: %#v", in)
	}
	if got := in.Parameters["speed_factor"]; got != float64(0.8) {
		t.Fatalf("speed_factor changed: %#v", in.Parameters)
	}
}

func TestNamedSpeechWAVRequiresActualCoherentPCMMetadata(t *testing.T) {
	valid := syntheticNamedSpeechWAV()
	rate, seconds, err := namedSpeechWAVInfo(valid)
	if err != nil || rate != 8000 || seconds != float64(2)/8000 {
		t.Fatal("valid WAV metadata rejected", rate, seconds, err)
	}
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"forged_riff_size", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:8], 10); return b }},
		{"zero_sample_rate", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[24:28], 0); return b }},
		{"forged_byte_rate", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[28:32], 8000); return b }},
		{"unsupported_float_format", func(b []byte) []byte { binary.LittleEndian.PutUint16(b[20:22], 3); return b }},
		{"unaligned_pcm_data", func(b []byte) []byte {
			b = b[:47]
			binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
			binary.LittleEndian.PutUint32(b[40:44], 3)
			return b
		}},
		{"truncated_data", func(b []byte) []byte { return b[:len(b)-1] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := namedSpeechWAVInfo(test.mutate(append([]byte(nil), valid...))); err == nil {
				t.Fatal("malformed WAV accepted")
			}
		})
	}
}

func TestNamedSpeechParametersRejectResourceAndExecutionOverrides(t *testing.T) {
	for _, value := range []map[string]any{
		{"nested": map[string]any{"refAudioPath": "hidden"}},
		{"streaming_mode": false},
		{"seed": math.Inf(1)},
		{"tuning": []any{map[string]any{"model": "alternate"}}},
		{"strategy": "alternate.ckpt"},
		{"strategy": "C:alternate"},
		{"strategy": "https://external.example"},
	} {
		if err := validateNamedSpeechParameters(value); err == nil {
			t.Fatal("resource-bearing parameter accepted", value)
		}
	}
	if err := validateNamedSpeechParameters(map[string]any{"sample_steps": 64, "seed": -1, "speed_factor": 1.1, "text_lang": "zh"}); err != nil {
		t.Fatal("ordinary engine tuning rejected", err)
	}
}
