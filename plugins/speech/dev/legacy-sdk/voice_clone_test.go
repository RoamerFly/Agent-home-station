package legacysdk

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func syntheticCloneWAV() []byte {
	b := make([]byte, 44+24000*2*5)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	copy(b[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], 1)
	binary.LittleEndian.PutUint16(b[22:24], 1)
	binary.LittleEndian.PutUint32(b[24:28], 24000)
	binary.LittleEndian.PutUint32(b[28:32], 48000)
	binary.LittleEndian.PutUint16(b[32:34], 2)
	binary.LittleEndian.PutUint16(b[34:36], 16)
	copy(b[36:40], "data")
	binary.LittleEndian.PutUint32(b[40:44], uint32(len(b)-44))
	return b
}

func TestCloneVoiceUploadsLocalAudioAndPreservesRemoteIdentity(t *testing.T) {
	posts := 0
	u := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		var body struct {
			Model string         `json:"model"`
			Input map[string]any `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "voice-enrollment" || body.Input["action"] != "create_voice" || body.Input["target_model"] != "qwen-audio-3.1-tts-plus" || body.Input["enable_preprocess"] != true || body.Input["enable_volume_normalization"] != "true" || !strings.HasPrefix(body.Input["audio"].(string), "data:audio/wav;base64,") {
			t.Error("incorrect clone contract")
		}
		w.Write([]byte(`{"output":{"voice_id":"synthetic-cloned-once"}}`))
	}))
	defer u.Close()
	cfg := Defaults()
	cfg.Endpoint, cfg.Key = u.URL, "synthetic.private.key"
	c := New(cfg)
	c.HTTP = u.Client()
	v, err := c.CloneVoice(context.Background(), "whaleRef", syntheticCloneWAV(), CloneOptions{true, true})
	if err != nil || v.ID != "synthetic-cloned-once" || posts != 1 {
		t.Fatal(v.ID, err, posts)
	}
	bad := syntheticCloneWAV()
	binary.LittleEndian.PutUint32(bad[40:44], 0x7fffff9b)
	if _, err = c.CloneVoice(context.Background(), "whaleRef", bad, CloneOptions{}); err == nil || posts != 1 {
		t.Fatal("streaming/incomplete WAV submitted")
	}
	if _, err = c.CloneVoice(context.Background(), "../invalid", syntheticCloneWAV(), CloneOptions{}); err == nil || posts != 1 {
		t.Fatal("invalid prefix submitted")
	}
}
