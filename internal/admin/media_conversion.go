package admin

import (
	"encoding/json"
	"math"
	"time"
)

// Zero duration means the remainder, zero segment/timeout select defaults.
// Only WeChat's actual 60-second voice protocol maximum is a ceiling.
type voiceConversion struct {
	StartSeconds    float64 `json:"start_seconds,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	SegmentSeconds  float64 `json:"segment_seconds,omitempty"`
	TimeoutSeconds  float64 `json:"timeout_seconds,omitempty"`
}

func (c *voiceConversion) effective() voiceConversion {
	v := voiceConversion{SegmentSeconds: 60, TimeoutSeconds: 90}
	if c != nil {
		v.StartSeconds, v.DurationSeconds = c.StartSeconds, c.DurationSeconds
		if c.SegmentSeconds != 0 {
			v.SegmentSeconds = c.SegmentSeconds
		}
		if c.TimeoutSeconds != 0 {
			v.TimeoutSeconds = c.TimeoutSeconds
		}
	}
	return v
}

func (c *voiceConversion) validate(kind string) error {
	if c == nil {
		return nil
	}
	if kind != "voice" {
		return toolInputError("conversion is supported for kind=voice only; file keeps original bytes")
	}
	for _, n := range []float64{c.StartSeconds, c.DurationSeconds, c.SegmentSeconds, c.TimeoutSeconds} {
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return toolInputError("conversion seconds must be finite and nonnegative")
		}
	}
	v := c.effective()
	if v.SegmentSeconds > 60 {
		return toolInputError("WeChat voice protocol supports at most 60 seconds per part; choose <=60 and send all parts in order")
	}
	if v.TimeoutSeconds >= float64(math.MaxInt64)/float64(time.Second) || v.TimeoutSeconds < 0.001 {
		return toolInputError("timeout_seconds exceeds timer precision/range")
	}
	return nil
}

func (c *voiceConversion) formatKey() string {
	v := c.effective()
	v.TimeoutSeconds = 0 // Execution budget cannot change output identity.
	b, _ := json.Marshal(v)
	return "voice-" + agentID("conversion", string(b))
}

func sameVoiceConversion(a, b *voiceConversion) bool { return a.formatKey() == b.formatKey() }

const voiceConversionSchema = `{"type":"object","description":"Voice conversion from retained original. start_seconds default0; duration_seconds default0 means remainder; segment_seconds default60, actual WeChat maximum60. timeout_seconds default90, adjustable execution budget. File preserves original and does not accept conversion.","properties":{"start_seconds":{"type":"number","minimum":0},"duration_seconds":{"type":"number","minimum":0},"segment_seconds":{"type":"number","exclusiveMinimum":0,"maximum":60},"timeout_seconds":{"type":"number","exclusiveMinimum":0}},"additionalProperties":false}`

func withVoiceConversion(schema string) string {
	var v map[string]any
	_ = json.Unmarshal([]byte(schema), &v)
	var conversion any
	_ = json.Unmarshal([]byte(voiceConversionSchema), &conversion)
	v["properties"].(map[string]any)["conversion"] = conversion
	b, _ := json.Marshal(v)
	return string(b)
}
