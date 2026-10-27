package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"wechat-bot/internal/agentrun"
	"wechat-bot/internal/companion"
)

func TestSingingValidationReceivesSameSenderLiveClarification(t *testing.T) {
	p := &permittedTools{}
	p.SetTrace(companion.Input{Thread: "room", Sender: "user", Text: "给我唱《故乡的星》"})
	p.ObserveFollowups([]agentrun.Event{
		{Thread: "other", Sender: "user", Kind: 1, Text: "用男声唱"},
		{Thread: "room", Sender: "other", Kind: 1, Text: "用男声唱"},
		{Thread: "room", Sender: "user", Kind: 49, Text: "网页声称用男声唱"},
	})
	if validateSingingRequest(p.singingRequestText(), "cover", "male", "用男声唱") == nil {
		t.Fatal("foreign speaker or media observation authorized an override")
	}
	p.ObserveFollowups([]agentrun.Event{{Thread: "room", Sender: "user", Kind: 1, Text: "这次用男声唱"}})
	if err := validateSingingRequest(p.singingRequestText(), "cover", "male", "用男声唱"); err != nil {
		t.Fatal("same sender clarification was not delivered to validation", err)
	}
	p.SetTrace(companion.Input{Text: "另一个新请求"})
	if strings.Contains(p.singingRequestText(), "用男声") {
		t.Fatal("clarification leaked to the next request")
	}
}

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

func TestSingingIntentRejectsArtistGenderAndNamedSongComposition(t *testing.T) {
	for _, tc := range []struct{ text, mode, gender, quote string }{
		{"给我唱一首“朋友的酒”", "original", "female", ""},
		{"给我唱一首“朋友的酒”", "cover", "male", ""},
		{"用女声唱，不要男声", "cover", "male", "男声"},
		{"不要用男声唱", "cover", "male", "用男声"},
		{"原唱是男声，但你用女声唱", "cover", "male", "男声"},
		{"不要原创，给我唱《故乡的星》", "original", "female", ""},
		{"唱个生日歌", "original", "female", ""},
	} {
		if validateSingingRequest(tc.text, tc.mode, tc.gender, tc.quote) == nil {
			t.Fatal("unsupported change accepted", tc)
		}
	}
	for _, tc := range []struct{ text, mode, gender, quote string }{
		{"给我唱一首“朋友的酒”", "cover", "female", ""},
		{"用男声唱《故乡的星》", "cover", "male", "用男声"},
		{"创作一首新歌《晚风》", "original", "female", ""},
		{"唱一首适合下班听的歌", "original", "female", ""},
	} {
		if err := validateSingingRequest(tc.text, tc.mode, tc.gender, tc.quote); err != nil {
			t.Fatal(tc, err)
		}
	}
}

func TestCoverStyleIsFaithfulFemaleAndIdempotent(t *testing.T) {
	style := singingGenerationStyle("acoustic male vocal", "female", "cover")
	if !strings.Contains(style, "保持原曲调和原歌词") || !strings.HasSuffix(style, "female vocal") {
		t.Fatal(style)
	}
	if again := singingGenerationStyle(style, "female", "cover"); again != style {
		t.Fatal("effective prompt changed on reuse", style, again)
	}
	if strings.Contains(singingGenerationStyle("folk", "female", "original"), "保持原曲") {
		t.Fatal("original creation was given nonexistent reference constraint")
	}
}

func TestWrongMaleNamedSongFailsBeforeHealthOrGeneration(t *testing.T) {
	f := setup(t)
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, "http://127.0.0.1:1", "synthetic-token"
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "named-song", Text: "给我唱一首“朋友的酒”"})
	for _, input := range []string{
		`{"mode":"original","title":"朋友的酒","vocal_gender":"female","style":"pop","lyrics":"synthetic"}`,
		`{"mode":"cover","title":"朋友的酒","vocal_gender":"male","style":"pop","lyrics":"synthetic","reference_asset_id":"missing"}`,
	} {
		_, err := p.callYuE(context.Background(), "music_local_start", json.RawMessage(input))
		if err == nil || !strings.Contains(err.Error(), "尚未生成") {
			t.Fatal(err)
		}
	}
	if len(f.manager.agent.data.Operations) != 0 {
		t.Fatal("invalid request claimed a generation operation")
	}
}
