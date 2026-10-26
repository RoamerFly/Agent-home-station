package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestSongDefaultsNativeFemaleEvenWithCharacterVoiceConfigured(t *testing.T) {
	starts := 0
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":4,"singing_voice_profile":"configured-C"}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["singing_voice"] != "native" || body["vocal_gender"] != "female" || strings.Contains(body["style"].(string), "male vocal,") || !strings.HasSuffix(body["style"].(string), "female vocal") {
			t.Error(body)
		}
		starts++
		w.Write([]byte(`{"id":"native-female-song","state":"queued"}`))
	}))
	defer u.Close()
	f := setup(t)
	f.manager.speech.YuEEnabled = true
	f.manager.speech.YuEEndpoint = u.URL
	f.manager.speech.YuEToken = "synthetic-token"
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "native-voice-test", Text: "唱一首下班主题的歌"})
	_, err := p.callYuE(context.Background(), "music_local_start", json.RawMessage(`{"mode":"original","title":"晚风归途","style":"warm male vocal, acoustic folk","lyrics":"[Verse]\n合成测试"}`))
	if err != nil || starts != 1 {
		t.Fatal(starts, err)
	}
}
