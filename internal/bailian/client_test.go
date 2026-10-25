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

func TestTurnExpressionPreservesConfiguredCharacterVoice(t *testing.T) {
	var input map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input map[string]any `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		input = body.Input
		w.Write([]byte(`{"output":{"audio":{"url":"https://audio.example/test.wav"}}}`))
	}))
	defer server.Close()
	cfg := Defaults()
	cfg.Endpoint, cfg.Key = server.URL, "synthetic.private.key"
	cfg.Voice, cfg.Instruction = "synthetic-character-C", "保持角色原声，俏皮元气"
	c := New(cfg)
	c.HTTP = server.Client()
	if _, err := c.Synthesize(context.Background(), "合成台词", "轻声安慰朋友"); err != nil {
		t.Fatal(err)
	}
	if input["voice"] != cfg.Voice || !strings.Contains(input["instruction"].(string), cfg.Instruction) || !strings.Contains(input["instruction"].(string), "轻声安慰朋友") {
		t.Fatal("turn expression replaced identity", input)
	}
	if _, err := c.Synthesize(context.Background(), "合成台词", ""); err != nil {
		t.Fatal(err)
	}
	if input["instruction"] != cfg.Instruction {
		t.Fatal("default expression changed")
	}
}
