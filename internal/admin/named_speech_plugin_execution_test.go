package admin

import (
	"encoding/binary"
	"math"
	"testing"
)

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
