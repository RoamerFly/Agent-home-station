package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

// Capture a fresh first decision using the configured provider; tool execution
// is stopped before network search/download. The original raw answer was not
// logged, so this reproduction must not be labelled as its exact old output.
func TestLiveVideoFirstDecisionShape(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" {
		t.Skip("explicit isolated model diagnostic")
	}
	c, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.config.Companion = c.Companion
	provider, e := f.manager.makeModel(c)
	if e != nil {
		t.Fatal(e)
	}
	first := ""
	calls := 0
	model := previewModel{chat: func(ctx context.Context, s string, turns []map[string]string) (string, error) {
		calls++
		if calls > 1 {
			return "", errors.New("first decision captured")
		}
		answer, e := provider.Chat(ctx, s, turns)
		first = answer
		return answer, e
	}}
	engine, e := companion.NewPreview(c.Companion, model, c.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	access, limit, e := f.manager.toolAccess()
	if e != nil {
		t.Fatal(e)
	}
	engine.Tools = firstDecisionTools{inner: access}
	engine.MaxToolCalls = limit
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	decision, decisionErr := engine.Respond(ctx, companion.Input{Channel: "isolated", Account: "test", Thread: "animation-probe", Mode: "direct", Preview: true, Directed: true, Text: "帮我找一个\"超兽武装\"的动画片段视频发给我"})
	report := map[string]any{"fresh_first_model_answer": first, "is_original_historical_output": false, "result_action": decision.Action, "result_reason": decision.Reason, "result_error": decisionErr != nil, "tool_execution_attempts": 0, "wechat_sends": 0}
	b, _ := json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(filepath.Join(workspace, "research/evidence/video-first-decision.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("fresh first answer: %s", first)
}

type firstDecisionTools struct{ inner *permittedTools }

func (t firstDecisionTools) SetTrace(in companion.Input) { t.inner.SetTrace(in) }
func (t firstDecisionTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	return t.inner.List(ctx)
}
func (t firstDecisionTools) Call(context.Context, string, json.RawMessage) (string, error) {
	return "", errors.New("probe stops before executing tool")
}

func TestLiveVideoRequestPublicTools(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_VIDEO_FULL") != "1" {
		t.Skip("explicit isolated public-video test")
	}
	c, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	native := &liveVideoStaging{fakeTools: &fakeTools{fakeNative: f.native}, dir: t.TempDir()}
	f.manager.native = native
	f.manager.config.Companion = c.Companion
	provider, e := f.manager.makeModel(c)
	if e != nil {
		t.Fatal(e)
	}
	answers := []string{}
	model := previewModel{chat: func(ctx context.Context, s string, turns []map[string]string) (string, error) {
		a, e := provider.Chat(ctx, s, turns)
		answers = append(answers, a)
		return a, e
	}}
	engine, e := companion.NewPreview(c.Companion, model, c.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	access, limit, e := f.manager.toolAccess()
	if e != nil {
		t.Fatal(e)
	}
	observed := &reuseObservedTools{inner: access}
	engine.Tools = observed
	engine.MaxToolCalls = limit
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	d, decisionErr := engine.Respond(ctx, companion.Input{Channel: "isolated", Account: "test", Thread: "animation-probe", Mode: "direct", Preview: true, Directed: true, Text: "帮我找一个\"超兽武装\"的动画片段视频发给我"})
	events := []map[string]any{}
	for _, x := range f.manager.Logs() {
		if x.Event == "web_activity" && x.Status != "running" {
			events = append(events, map[string]any{"tool": x.Tool, "status": x.Status, "reason": x.Reason, "url": x.TargetURL, "query": x.Query, "bytes": x.Bytes})
		}
	}
	var selected companion.GeneratedMedia
	selectedOK := false
	if len(d.MediaIDs) == 1 {
		selected, selectedOK = engine.Media(d.MediaIDs[0])
	}
	report := map[string]any{"fresh_model_answers": answers, "decision": d, "error": decisionErr != nil, "tool_calls": observed.calls, "events": events, "wechat_sends": native.submits.Load(), "isolated_native_staging": true, "selected_media": selected, "prepared_video": native.probe, "prepared_video_bytes": native.bytes, "prepared_video_sha256": native.hash}
	b, _ := json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(filepath.Join(workspace, "research/evidence/video-public-current-policy.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("decision action=%s reason=%s media=%d calls=%v err=%v", d.Action, d.Reason, len(d.MediaIDs), observed.calls, decisionErr)
	if decisionErr != nil || d.Action != "reply" || !selectedOK || native.submits.Load() != 0 {
		t.Fatal("live video chain did not complete")
	}
	switch selected.Kind {
	case "share":
		if selected.URL == "" || selected.DurationMS <= 60000 || native.bytes != 0 {
			t.Fatal("long video must use a verified playback card without uploading video")
		}
	case "video":
		if native.bytes < 10000 || selected.DurationMS > 60000 {
			t.Fatal("short video missing or exceeds inline duration policy")
		}
	default:
		t.Fatal("selected video or playback card missing")
	}
}

type liveVideoStaging struct {
	*fakeTools
	dir   string
	probe json.RawMessage
	bytes int
	hash  string
}

func (n *liveVideoStaging) Upload(ctx context.Context, name, typ string, r io.Reader, size int64) (linuxnative.UploadSource, error) {
	b, e := io.ReadAll(io.LimitReader(r, toolsLimit("video")+1))
	if e != nil || int64(len(b)) != size || len(b) < 12 || string(b[4:8]) != "ftyp" {
		return linuxnative.UploadSource{}, errors.New("not a complete MP4")
	}
	file := filepath.Join(n.dir, "prepared.mp4")
	if e = os.WriteFile(file, b, 0600); e != nil {
		return linuxnative.UploadSource{}, e
	}
	output, e := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height:format=duration", "-of", "json", file).Output()
	if e != nil {
		return linuxnative.UploadSource{}, e
	}
	var p struct {
		Streams []struct {
			Type string `json:"codec_type"`
		} `json:"streams"`
	}
	if json.Unmarshal(output, &p) != nil {
		return linuxnative.UploadSource{}, errors.New("bad video metadata")
	}
	video := false
	for _, s := range p.Streams {
		video = video || s.Type == "video"
	}
	if !video {
		return linuxnative.UploadSource{}, errors.New("video stream absent")
	}
	n.probe = output
	n.bytes = len(b)
	sum := sha256.Sum256(b)
	n.hash = hex.EncodeToString(sum[:])
	return linuxnative.UploadSource{ID: fmt.Sprintf("verified-video-%d", n.bytes), Filename: name, ContentType: typ, ByteLength: size}, nil
}
