package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/companion"
)

// Read only the explicitly selected cached audio; no production task ledger is
// opened and no message is sent. Transcript is private evidence, not a fixture.
func TestLiveReportedReferenceLyricsWithoutWeChatSend(t *testing.T) {
	root, workspace, id := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE"), os.Getenv("WECHAT_REPORTED_REFERENCE")
	if root == "" || id == "" || os.Getenv("WECHAT_REFERENCE_ASR_LIVE") != "1" {
		t.Skip("explicit owner-requested reference transcription")
	}
	b, err := os.ReadFile(filepath.Join(root, "admin/media-assets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var assets []mediaAsset
	if json.Unmarshal(b, &assets) != nil {
		t.Fatal("invalid asset index")
	}
	var original *mediaAsset
	for i := range assets {
		if assets[i].ID == id {
			original = &assets[i]
			break
		}
	}
	if original == nil {
		t.Fatal("selected reference absent")
	}
	data, err := (&Manager{dir: filepath.Join(root, "admin")}).cachedAssetData(*original)
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.speech, err = loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "isolated-reference-inspection"}
	asset, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: data, MIME: original.MIME, Filename: original.Filename, URL: original.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(in)
	if os.Getenv("WECHAT_REFERENCE_ASR_FULL") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		wav, err := normalizeSpeech(ctx, f.dir, data)
		if err != nil {
			t.Fatal(err)
		}
		text, err := bailian.New(f.manager.speech).Transcribe(ctx, wav)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(map[string]any{"reference_asset_id": id, "source_url": original.URL, "full_transcript": text, "warning": "ASR candidate from complete reference; not independently lyric-verified", "wechat_sends": 0}, "", "  ")
		if err = os.WriteFile(filepath.Join(workspace, "research/evidence/reported-cover-full-lyrics.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	report := []map[string]any{}
	for _, offset := range []int{0, 60} {
		args, _ := json.Marshal(map[string]any{"asset_id": asset.ID, "transcribe": true, "offset_seconds": offset, "seconds": 60})
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		result, err := p.inspectMusicReference(ctx, args)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if json.Unmarshal([]byte(result), &body) != nil || body["transcript"] == "" {
			t.Fatal("no transcript")
		}
		report = append(report, body)
	}
	if workspace != "" {
		b, _ := json.MarshalIndent(map[string]any{"reference_asset_id": id, "source_url": original.URL, "observations": report, "wechat_sends": 0}, "", "  ")
		if err := os.WriteFile(filepath.Join(workspace, "research/evidence/reported-cover-transcript.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLiveDiagnosticCoverOutputRecognition(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_COVER_OUTPUT_LIVE") != "1" {
		t.Skip("explicit completed diagnostic only; does not generate or send")
	}
	reportName, outputName, proofName := "reported-cover-diagnostic.json", "忘情牛肉面-参考翻唱30秒诊断.flac", "reported-cover-output-observation.json"
	if os.Getenv("WECHAT_COVER_OUTPUT_FULL") == "1" {
		reportName, outputName, proofName = "reported-cover-full-female.json", "大肥鱼翻唱-忘情牛肉面-完整女声版.flac", "reported-cover-full-output-observation.json"
	}
	b, err := os.ReadFile(filepath.Join(workspace, "research/evidence", reportName))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal(b, &report) != nil || !validID(report.JobID) {
		t.Fatal("diagnostic receipt missing")
	}
	f := setup(t)
	f.manager.speech, err = loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := f.manager.toolAccess()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	status, _, err := p.yueRequest(ctx, "GET", "/jobs/"+report.JobID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]any
	if json.Unmarshal(status, &job) != nil || job["state"] != "completed" || job["reference_used"] != true {
		t.Fatal("not a completed reference-conditioned job")
	}
	audio, _, err := p.yueRequest(ctx, "GET", "/jobs/"+report.JobID+"/audio", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	wav, err := normalizeSpeech(ctx, f.dir, audio)
	if err != nil {
		t.Fatal(err)
	}
	text, err := bailian.New(f.manager.speech).Transcribe(ctx, wav)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := json.MarshalIndent(map[string]any{"job": job, "audio_bytes": len(audio), "generated_audio_asr": text, "wechat_sends": 0, "quality_boundary": "ASR observation only, not a listening or melody-fidelity guarantee"}, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence", proofName), proof, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence", outputName), audio, 0600); err != nil {
		t.Fatal(err)
	}
}
