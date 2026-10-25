package bailian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpeechContractsAndPrivateErrors(t *testing.T) {
	count := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Bearer synthetic.private.key" {
			t.Error("key truncated")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(r.URL.Path, "SpeechSynthesizer") {
			if body["model"] != "qwen-audio-3.1-tts-plus" {
				t.Error(body)
			}
			w.Write([]byte(`{"output":{"audio":{"url":"https://audio.example/test.wav"}}}`))
		} else {
			w.Write([]byte(`{"output":{"output":{"text":"你好小鲸鱼"}}}`))
		}
	}))
	defer server.Close()
	cfg := Defaults()
	cfg.Endpoint = server.URL
	cfg.Key = "synthetic.private.key"
	c := New(cfg)
	c.HTTP.Transport = server.Client().Transport
	if u, e := c.Synthesize(context.Background(), "你好", ""); e != nil || u == "" {
		t.Fatal(u, e)
	}
	if text, e := c.Transcribe(context.Background(), []byte("RIFF-test")); e != nil || text != "你好小鲸鱼" {
		t.Fatal(text, e)
	}
	if count != 2 {
		t.Fatal(count)
	}
}
