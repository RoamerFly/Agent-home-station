package mobile

import (
	"net/url"
	"strings"
	"testing"
)

func TestValidStepProtocol(t *testing.T) {
	// Construct synthetic userinfo through the URL API; this is never fetched.
	credentialURL := &url.URL{Scheme: "https", Host: "example.test", Path: "/", User: url.UserPassword("synthetic", "not-a-real-credential")}
	observation := Observation{
		Width:           100,
		Height:          200,
		AllowedPackages: []string{"com.example.home", "com.example.browser"},
		Browsers:        []string{"com.example.browser"},
		WebHosts:        []string{"example.test", "8.8.8.8"},
	}
	cases := []struct {
		name string
		step Step
		want bool
	}{
		{"finish without arguments", Step{Action: "finish", Summary: "完成"}, true},
		{"finish rejects injection", Step{Action: "finish", Arguments: map[string]any{"device_id": "attacker"}}, false},
		{"capture actual range", Step{Action: "capture", Arguments: map[string]any{"seconds": float64(12)}}, true},
		{"capture fractional", Step{Action: "capture", Arguments: map[string]any{"seconds": float64(2.5)}}, false},
		{"capture unrelated parameter", Step{Action: "capture", Arguments: map[string]any{"seconds": float64(3), "request_key": "replay"}}, false},
		{"allowed launch", Step{Action: "launch", Arguments: map[string]any{"package": "com.example.home"}}, true},
		{"unobserved launch", Step{Action: "launch", Arguments: map[string]any{"package": "com.example.other"}}, false},
		{"valid browser URL", Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.browser", "url": "https://example.test/path?q=1"}}, true},
		{"valid default https port", Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.browser", "url": "https://example.test:443/"}}, true},
		{"unobserved host", Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.browser", "url": "https://other.test/"}}, false},
		{"credentials rejected", Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.browser", "url": credentialURL.String()}}, false},
		{"nonstandard port rejected", Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.browser", "url": "https://example.test:8443/"}}, false},
		{"private literal rejected", Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.browser", "url": "https://127.0.0.1/"}}, false},
		{"localhost rejected even if listed", Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.browser", "url": "https://localhost/"}}, false},
		{"global literal permitted only when listed", Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.browser", "url": "https://8.8.8.8/"}}, true},
		{"unicode text", Step{Action: "text", Arguments: map[string]any{"text": "你好，设备"}}, true},
		{"empty text", Step{Action: "text", Arguments: map[string]any{"text": ""}}, false},
		{"tap in bounds", Step{Action: "tap", Arguments: map[string]any{"x": float64(99), "y": float64(199)}}, true},
		{"tap boundary rejected", Step{Action: "tap", Arguments: map[string]any{"x": float64(100), "y": float64(0)}}, false},
		{"tap fraction rejected", Step{Action: "tap", Arguments: map[string]any{"x": float64(1.5), "y": float64(2)}}, false},
		{"swipe in bounds", Step{Action: "swipe", Arguments: map[string]any{"x": float64(0), "y": float64(0), "x2": float64(99), "y2": float64(199)}}, true},
		{"swipe optional duration", Step{Action: "swipe", Arguments: map[string]any{"x": float64(0), "y": float64(0), "x2": float64(99), "y2": float64(199), "duration_ms": float64(1500)}}, true},
		{"swipe unsupported argument", Step{Action: "swipe", Arguments: map[string]any{"x": float64(0), "y": float64(0), "x2": float64(1), "y2": float64(1), "action": "tap"}}, false},
		{"summary rune bound", Step{Action: "wait", Summary: string(make([]rune, 801))}, false},
		{"summary Unicode protocol bound", Step{Action: "wait", Summary: strings.Repeat("📝", 800)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidStep(tc.step, observation); got != tc.want {
				t.Fatalf("ValidStep(%#v) = %v, want %v", tc.step, got, tc.want)
			}
		})
	}
}
