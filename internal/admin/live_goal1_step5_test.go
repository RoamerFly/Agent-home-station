package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type liveResourceStaging struct {
	*fakeNative
	uploads int
	bytes   int64
}

func (*liveResourceStaging) Capabilities(context.Context) (linuxnative.Capabilities, error) {
	return linuxnative.Capabilities{MediaKinds: []string{"voice"}}, nil
}
func (n *liveResourceStaging) Upload(_ context.Context, name, mime string, r io.Reader, size int64) (linuxnative.UploadSource, error) {
	b, err := io.ReadAll(r)
	if err != nil || int64(len(b)) != size || len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return linuxnative.UploadSource{}, fmt.Errorf("prepared media was not complete WAV audio")
	}
	n.uploads++
	n.bytes += size
	return linuxnative.UploadSource{ID: fmt.Sprintf("isolated-audio-%d", n.uploads), Filename: name, ContentType: mime, ByteLength: size}, nil
}
func (*liveResourceStaging) Submit(context.Context, linuxnative.ToolSend) (linuxnative.ToolStatus, error) {
	panic("live resource regression must never send WeChat messages")
}
func (*liveResourceStaging) LookupSend(context.Context, string) (linuxnative.ToolStatus, error) {
	panic("unexpected delivery lookup")
}
func (*liveResourceStaging) ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error) {
	panic("unexpected delivery status")
}

func TestLiveGoal1ResourceSearchDownloadAndModelChoice(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_GOAL1_ROOT"), os.Getenv("WECHAT_GOAL1_WORKSPACE")
	if root == "" || workspace == "" {
		t.Skip("explicit isolated public resource test")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("private config unavailable")
	}
	f := setup(t)
	native := &liveResourceStaging{fakeNative: f.native}
	f.manager.native = native
	f.manager.config = c
	f.manager.config.Enabled = false
	f.manager.config.Companion.Permissions.SendVoice = true
	access, limit, err := f.manager.toolAccess()
	if err != nil || access == nil {
		t.Fatal("tools unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	access.SetTrace(companion.Input{Channel: "isolated", Account: native.Account(), Thread: "hypothetical", TraceID: "goal1-isolated-resource"})
	start := time.Now()
	result, err := access.Call(ctx, "web_audio_search", json.RawMessage(`{"query":"Happy Alley"}`))
	if err != nil {
		t.Fatal("exact catalog search failed", mcptools.FailureDescription(err))
	}
	var catalog struct{ Candidates []struct{ Title, URL string } }
	if json.Unmarshal([]byte(result), &catalog) != nil {
		t.Fatal("catalog not readable")
	}
	exact := ""
	for _, item := range catalog.Candidates {
		if item.Title == "Happy Alley" {
			exact = item.URL
		}
	}
	if exact == "" {
		t.Fatal("exact catalog entry missing")
	}
	t.Logf("Exact public catalog resolved in %.1fs", time.Since(start).Seconds())
	args, _ := json.Marshal(map[string]string{"url": exact, "kind": "voice"})
	start = time.Now()
	_, err = access.Call(ctx, "web_media_download", args)
	if err != nil {
		for _, entry := range f.manager.Logs() {
			if entry.Event == "web_activity" && entry.Status == "failed" {
				t.Logf("safe tool diagnostics: code=%s stage=%s http=%d requests=%d retries=%d reason=%s", entry.ToolCode, entry.ToolStage, entry.ToolHTTPStatus, entry.ToolRequests, entry.ToolRetries, entry.Reason)
			}
		}
		t.Fatalf("real audio download/prepare failed (%T): %s", err, mcptools.FailureDescription(err))
	}
	if native.uploads != 1 || native.bytes < 1<<20 {
		t.Fatal("full audio not prepared", native.uploads, native.bytes)
	}
	downloadSeconds := time.Since(start).Seconds()
	t.Logf("Real public audio prepared: bytes=%d elapsed=%.1fs; staged only in fake native", native.bytes, downloadSeconds)
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal("provider unavailable")
	}
	config := c.Companion
	config.Enabled = true
	config.Permissions.SendVoice = true
	engine, err := companion.NewPreview(config, provider, c.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	// Use a fresh staging map so the model cannot re-use the prior diagnostic's
	// media ID. It must choose tools and acquire a second, real attachment.
	engine.Tools, engine.MaxToolCalls, err = f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	decision, err := engine.Respond(ctx, companion.Input{Mode: "direct", Preview: true, Text: "请把 Kevin MacLeod 的 Happy Alley 作为微信语音发给我。这是一首公开器乐，可以查公开音乐目录，保留真实曲名作者和来源；请实际取得文件。"})
	if err != nil {
		t.Fatal("configured model failed", deepseek.UserMessage(err))
	}
	if decision.Action != "reply" || len(decision.MediaIDs) == 0 || native.uploads < 2 {
		t.Fatal("model did not acquire requested media", decision.Action, decision.Reason, len(decision.MediaIDs), native.uploads)
	}
	for _, id := range decision.MediaIDs {
		if _, ok := engine.Media(id); !ok {
			t.Fatal("model selected nonexistent media")
		}
	}
	if strings.Contains(strings.Join(decision.Messages, " "), "工具调用") {
		t.Fatal("tool protocol leaked into reply")
	}
	logs := f.manager.Logs()
	activities := []map[string]any{}
	for _, item := range logs {
		if item.Event == "web_activity" && item.Status != "running" {
			activities = append(activities, map[string]any{"tool": item.Tool, "status": item.Status, "duration_ms": item.DurationMS, "bytes": item.Bytes, "media_count": item.MediaCount, "get_attempts": item.ToolRequests, "get_retries": item.ToolRetries, "last_failure_code": item.ToolCode, "last_failure_stage": item.ToolStage})
		}
	}
	report := map[string]any{"exact_track": "Happy Alley", "real_catalog_and_download": true, "prepared_wav_bytes": native.bytes, "staging_uploads": native.uploads, "isolated_native_only": true, "configured_provider_live": true, "model_selected_real_media": true, "model_media_count": len(decision.MediaIDs), "configured_tool_call_limit": limit, "activities": activities, "wechat_sends": 0, "production_memory_modified": false}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence/goal1-step5-live-resource.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(b))
}
