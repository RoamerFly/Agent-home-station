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

func TestDirectRVCCoverUsesWholeActualRecordingWithoutYuEOrASRLyrics(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "rvc-request", Mode: "direct", Text: "翻唱这首"}
	p.SetTrace(in)
	asset, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: syntheticReferenceWAV(t, 72), MIME: "audio/wav", Filename: "synthetic-original.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	posts := 0
	var submitted map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rvc-health":
			w.Write([]byte(`{"ready":true,"bridge_version":9}`))
		case "/rvc-jobs":
			posts++
			json.NewDecoder(r.Body).Decode(&submitted)
			w.Write([]byte(`{"id":"synthetic-rvc-job","engine":"rvc","state":"queued"}`))
		case "/jobs/synthetic-rvc-job":
			w.Write([]byte(`{"id":"synthetic-rvc-job","engine":"rvc","state":"running"}`))
		default:
			t.Error("RVC called a YuE generation endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer upstream.Close()
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, upstream.URL, "synthetic-token"
	p.settings.Enabled = false // A local music tool is independent of web/MCP switches.
	raw, _ := json.Marshal(map[string]any{"title": "原曲换声", "reference_asset_id": asset.ID})
	for i := 0; i < 2; i++ {
		result, err := p.Call(context.Background(), "music_rvc_start", raw)
		if err != nil || !strings.Contains(result, `"automatic_followup":true`) {
			t.Fatal(result, err)
		}
	}
	if posts != 1 || submitted["reference_seconds"] != float64(72) || submitted["reference_pretrimmed"] != true || submitted["reference_source_sha256"] != asset.Hash {
		t.Fatal("source length/identity lost or conversion replayed", posts, submitted)
	}
	if _, ok := submitted["lyrics"]; ok {
		t.Fatal("RVC used rewritten lyrics")
	}
	if _, ok := submitted["style"]; ok {
		t.Fatal("RVC used a composition prompt")
	}
	tasks := f.manager.agent.list("test", "filehelper")
	if len(tasks) != 1 || tasks[0].MusicSpec.Engine != "rvc" || tasks[0].MusicSpec.ReferenceSeconds != 72 {
		t.Fatal(tasks)
	}
	status, err := p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"synthetic-rvc-job"}`))
	if err != nil || !strings.Contains(status, `"running"`) {
		t.Fatal("RVC job not readable by normal continuation", status, err)
	}
	p.trace.Thread = "other-room"
	if _, err = p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"synthetic-rvc-job"}`)); err == nil {
		t.Fatal("cross-room RVC job leaked")
	}
	if _, err = p.callYuE(context.Background(), "music_rvc_start", raw); err == nil {
		t.Fatal("cross-room source readable")
	}
}
