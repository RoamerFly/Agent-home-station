package bailian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAudioReviewUsesActualAudioAndRequiresCompletedStream(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		valid        bool
	}{
		{"complete", "data: {\"choices\":[{\"delta\":{\"content\":\"合成听感测试\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true},
		{"incomplete", "data: {\"choices\":[{\"delta\":{\"content\":\"未完成\"}}]}\n\n", false},
		{"length_limit", "data: {\"choices\":[{\"delta\":{\"content\":\"截断\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/compatible-mode/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Error("wrong route/auth")
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				b, _ := json.Marshal(body)
				if !strings.Contains(string(b), "data:audio/wav;base64,") || body["stream"] != true || body["model"] != AudioReviewModel {
					t.Error("missing actual audio")
				}
				w.Write([]byte(tc.stream))
			}))
			defer s.Close()
			c := New(Config{Endpoint: s.URL, Key: "synthetic-key"})
			text, err := c.ReviewAudio(context.Background(), make([]byte, 64), "synthetic question")
			if tc.valid && (err != nil || text != "合成听感测试") {
				t.Fatal(text, err)
			}
			if !tc.valid && err == nil {
				t.Fatal("accepted incomplete review")
			}
		})
	}
}
