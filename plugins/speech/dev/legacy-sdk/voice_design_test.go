package legacysdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVoiceDesignCreatesBoundVoiceWithoutLeakingBinary(t *testing.T) {
	posts := 0
	u := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		input, _ := body["input"].(map[string]any)
		if r.URL.Path != "/api/v1/services/audio/tts/customization" || body["model"] != "voice-enrollment" || input["target_model"] != "qwen-audio-3.1-tts-plus" || input["action"] != "create_voice" {
			t.Error("wrong provider contract")
		}
		json.NewEncoder(w).Encode(map[string]any{"output": map[string]any{"voice_id": "synthetic-designed-voice", "target_model": "qwen-audio-3.1-tts-plus", "preview_audio": map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("synthetic mp3")), "sample_rate": 24000, "response_format": "mp3"}}})
	}))
	defer u.Close()
	cfg := Defaults()
	cfg.Endpoint = u.URL
	cfg.Key = "synthetic.private.key"
	c := New(cfg)
	c.HTTP = u.Client()
	v, err := c.DesignVoice(context.Background(), "whaleA", "清透自然的成年女声", "你好呀，我是蓝色大肥鱼，今天也想陪你聊点有趣的事情。")
	if err != nil || v.ID == "" || string(v.Preview) != "synthetic mp3" || posts != 1 {
		t.Fatal(v.ID, err, posts)
	}
	b, _ := json.Marshal(v)
	var saved map[string]any
	json.Unmarshal(b, &saved)
	if _, ok := saved["Preview"]; ok {
		t.Fatal("binary serialized into metadata")
	}
	if _, err = c.DesignVoice(context.Background(), "../invalid", "prompt", "short"); err == nil || posts != 1 {
		t.Fatal("invalid design submitted")
	}
}
func TestVoiceDesignKeepsCreatedIDWhenPreviewIsInvalid(t *testing.T) {
	u := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":{"voice_id":"created-once","target_model":"qwen-audio-3.1-tts-plus","preview_audio":{"data":"bad%","response_format":"mp3","sample_rate":24000}}}`))
	}))
	defer u.Close()
	cfg := Defaults()
	cfg.Endpoint = u.URL
	cfg.Key = "synthetic.private.key"
	c := New(cfg)
	c.HTTP = u.Client()
	v, err := c.DesignVoice(context.Background(), "whaleB", "自然俏皮女声", "你好呀，我是蓝色大肥鱼，今天也想陪你聊点有趣的事情。")
	if err == nil || v.ID != "created-once" {
		t.Fatal("lost remote side effect identity", v.ID, err)
	}
}

func TestVoiceDesignRecoversHostedPreviewWithoutAnotherCreation(t *testing.T) {
	for _, missingModel := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing-model", false: "different-preview-format"}[missingModel], func(t *testing.T) {
			creates, queries := 0, 0
			u := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Input map[string]any `json:"input"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				if body.Input["action"] == "query_voice" {
					queries++
					json.NewEncoder(w).Encode(map[string]any{"output": map[string]any{"voice_id": "existing-voice", "target_model": "qwen-audio-3.1-tts-plus", "status": "OK", "resource_link": "https://example.com/preview.wav"}})
					return
				}
				creates++
				output := map[string]any{"voice_id": "existing-voice"}
				if !missingModel {
					output["target_model"] = "qwen-audio-3.1-tts-plus"
					output["preview_audio"] = map[string]any{"data": "provider-wave", "sample_rate": 24000, "response_format": "wav"}
				}
				json.NewEncoder(w).Encode(map[string]any{"output": output})
			}))
			defer u.Close()
			cfg := Defaults()
			cfg.Endpoint, cfg.Key = u.URL, "synthetic.private.key"
			c := New(cfg)
			c.HTTP = u.Client()
			v, err := c.DesignVoice(context.Background(), "whaleC", "温柔自然的成年女声", "你好呀，我是蓝色大肥鱼，今天也想陪你聊点有趣的事情。")
			if err != nil || v.PreviewURL != "https://example.com/preview.wav" || v.Model != cfg.TTSModel || creates != 1 || queries != 1 {
				t.Fatal("recovery must query the existing voice", err, creates, queries)
			}
		})
	}
}

func TestInspectDesignedVoiceRejectsMismatchedBinding(t *testing.T) {
	u := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":{"voice_id":"existing-voice","target_model":"different-model","status":"OK","resource_link":"https://example.com/preview.wav"}}`))
	}))
	defer u.Close()
	cfg := Defaults()
	cfg.Endpoint, cfg.Key = u.URL, "synthetic.private.key"
	c := New(cfg)
	c.HTTP = u.Client()
	if _, err := c.InspectDesignedVoice(context.Background(), "existing-voice"); err == nil {
		t.Fatal("cross-model voice binding accepted")
	}
}
