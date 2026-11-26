package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
)

func TestStationComfyHTTPFailureGivesSafeActionableFeedback(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		code       string
		field      string
		notStarted bool
	}{
		{
			name: "missing input field", status: http.StatusUnprocessableEntity,
			body: `{"error":"missing required input at C:\\private\\model","code":"missing_required_input","field":"inputs.input_image"}`,
			code: "station_comfy_input_missing", field: "inputs.input_image", notStarted: true,
		},
		{
			name: "run result not saved", status: http.StatusConflict,
			body: `{"error":"ComfyUI 作业已执行但结果未能保存，请按原请求编号查询"}`,
			code: "station_comfy_run_outcome_unknown", notStarted: false,
		},
		{
			name: "untrusted service error", status: http.StatusServiceUnavailable,
			body: `{"error":"token=never-expose C:\\private\\model.safetensors"}`,
			code: "station_comfy_service_unavailable", notStarted: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/local/project-tools/comfyui/action" || r.Method != http.MethodPost {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			p := &permittedTools{}
			_, err := p.stationToolRequest(context.Background(), stationToolSession{BaseURL: server.URL, Cookie: "session=test", CSRF: "test"},
				http.MethodPost, "/api/local/project-tools/comfyui/action", map[string]any{"request_id": "wechat-comfy-original", "action": "run", "workflow_id": "h3-image-to-video"})
			var failure *stationComfyFailure
			if !errors.As(err, &failure) {
				t.Fatalf("expected structured failure, got %v", err)
			}
			code, feedback := failure.SafeToolFailure()
			if got := mcptools.FailureDescription(err); got != feedback {
				t.Fatalf("Agent-safe failure path changed feedback: %q", got)
			}
			if code != tc.code || failure.HTTPStatus != tc.status || failure.Action != "run" || failure.Field != tc.field || failure.NotStarted != tc.notStarted || failure.RequestID != "wechat-comfy-original" {
				t.Fatalf("failure=%+v", failure)
			}
			var visible map[string]any
			if json.Unmarshal([]byte(feedback), &visible) != nil || visible["code"] != tc.code || visible["next_action"] == "" {
				t.Fatalf("unsafe or incomplete feedback: %s", feedback)
			}
			if strings.Contains(feedback, "never-expose") || strings.Contains(feedback, "private") || strings.Contains(feedback, "model.safetensors") {
				t.Fatalf("upstream error leaked: %s", feedback)
			}
			if !tc.notStarted && !strings.Contains(failure.NextAction, "原 request_id") {
				t.Fatalf("unknown run outcome did not require original-operation check: %+v", failure)
			}
		})
	}
}
