package admin

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type fullMusicStager struct {
	*fakeTools
	Data  []byte
	MIME  string
	Index int
}

func (n *fullMusicStager) Upload(_ context.Context, name, mime string, r io.Reader, size int64) (linuxnative.UploadSource, error) {
	data, e := io.ReadAll(r)
	n.Data = data
	n.MIME = mime
	n.Index++
	return linuxnative.UploadSource{ID: "source-" + strconv.Itoa(n.Index), Filename: name, ContentType: mime, ByteLength: size}, e
}

func TestLiveMusicOneMinuteControlsDurationAndDefaultsToCompleteVoice(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_MUSIC_DURATION_LIVE") != "1" {
		t.Skip("explicit one-minute local music generation, no WeChat sends")
	}
	cfg, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	f.manager.config = cfg
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Remember = true
	n := &fullMusicStager{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = n
	legacySpeechSource, e := loadSpeech(filepath.Join(root, "admin"))
	if e == nil {
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechSource)
	}
	if e != nil {
		t.Fatal(e)
	}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "duration-proof-20261025", Text: "重新唱一首一分钟左右、适合下班听的歌"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, e := p.Call(ctx, "music_local_start", json.RawMessage(`{"title":"晚风下班路","style":"relaxed upbeat Chinese female acoustic pop for the commute after work, 100 BPM, short introduction, verse and chorus, about one minute with a clear ending","lyrics":"[verse]\n把今天的忙碌轻轻放下\n让晚风吹过亮起的街巷\n耳机里的节拍陪我回家\n车窗外的灯光慢慢变暖\n[chorus]\n下班啦就把心情松绑\n把疲惫留给身后的楼房\n走过街角看看晚霞\n我的快乐已经在路上\n明天的事等到明天再想\n今晚就让自己轻轻发光","duration_seconds":60,"kind":"voice"}`))
	if e != nil {
		t.Fatal(e)
	}
	var job struct {
		ID string `json:"job_id"`
	}
	json.Unmarshal([]byte(result), &job)
	if job.ID == "" {
		t.Fatal(result)
	}
	tasks := f.manager.agent.list("test", "filehelper")
	if len(tasks) != 1 || tasks[0].MusicKind != "voice" {
		t.Fatal("unsolicited file mode", tasks)
	}
	report := map[string]any{"job_id": job.ID, "requested_by_user_seconds": 60, "model_requested_seconds": 60, "kind": "voice", "wechat_sends": 0}
	save := func() {
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(workspace, "research/evidence/music-duration-live.json"), b, 0600)
	}
	save()
	var status struct {
		State    string
		Duration float64 `json:"duration_seconds"`
		Target   float64 `json:"requested_seconds"`
	}
	for {
		data, _, err := p.yueRequest(ctx, "GET", "/jobs/"+job.ID, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(data, &status)
		if status.State == "completed" {
			break
		}
		if status.State == "failed" || status.State == "needs_review" {
			report["state"] = status.State
			save()
			t.Fatal(string(data))
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	report["actual_generated_seconds"] = status.Duration
	report["generator_limit_seconds"] = status.Target
	save()
	if status.Target != 60 || status.Duration > 60.02 || status.Duration < 45 {
		t.Fatal("generation duration not respected", status)
	}
	if _, e = p.Call(ctx, "music_local_status", json.RawMessage(`{"job_id":"`+job.ID+`","kind":"voice"}`)); e != nil {
		t.Fatal(e)
	}
	if n.MIME != "audio/wav" {
		t.Fatal("default delivery became file", n.MIME)
	}
	wav := filepath.Join(f.dir, "delivered.wav")
	os.WriteFile(wav, n.Data, 0600)
	meta, e := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", wav).Output()
	if e != nil {
		t.Fatal(e)
	}
	seconds, _ := strconv.ParseFloat(strings.TrimSpace(string(meta)), 64)
	report["prepared_voice_seconds"] = seconds
	report["complete_audio_prepared"] = seconds >= status.Duration-0.1
	save()
	if seconds > 60.02 || seconds < status.Duration-0.1 {
		t.Fatal("default preparation truncated the song", seconds, status.Duration)
	}
	assets, _ := f.manager.loadAssets()
	if len(assets) != 1 {
		t.Fatal("original file not cached")
	}
	reuse, e := p.reuseAsset(ctx, assets[0].ID, "file")
	if e != nil || !strings.Contains(reuse, `"network_downloads":0`) {
		t.Fatal("original file reuse failed", e)
	}
	report["original_file_reusable_without_download"] = true
	save()
}
