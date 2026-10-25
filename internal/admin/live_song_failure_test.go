package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

type songFailureProbe struct {
	*deepseek.Client
	Rounds []map[string]any
}

func (p *songFailureProbe) ChatFunctions(ctx context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	answer, err := p.Client.ChatFunctions(ctx, system, turns, tools)
	row := map[string]any{"response_bytes": len(answer)}
	if err != nil {
		row["error"] = err.Error()
	} else {
		var call struct {
			Action, Tool string
			Arguments    json.RawMessage
			Calls        []struct{ Tool string }
		}
		_ = json.Unmarshal([]byte(answer), &call)
		row["action"] = call.Action
		row["tool"] = call.Tool
		row["argument_bytes"] = len(call.Arguments)
		if call.Action == "tool" && call.Tool != "discover_tools" {
			err = errors.New("diagnostic_stopped_before_tool_execution")
		}
		if call.Action == "tool_batch" {
			names := []string{}
			for _, item := range call.Calls {
				names = append(names, item.Tool)
				if item.Tool != "discover_tools" {
					err = errors.New("diagnostic_stopped_before_tool_execution")
				}
			}
			row["batch_tools"] = names
		}
	}
	p.Rounds = append(p.Rounds, row)
	return answer, err
}

func TestLiveReportedSongFailureWithoutExternalActions(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_SONG_FAILURE_LIVE") != "1" {
		t.Skip("explicit no-effect song failure diagnosis")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.speech, err = loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.native = &fakeTools{fakeNative: f.native}
	provider, err := f.manager.makeModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	probe := &songFailureProbe{Client: provider}
	c := cfg.Companion
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.Permissions.Remember = true
	tools, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(c, f.manager.memory, probe, cfg.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = tools
	engine.MaxToolCalls = 8
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, respondErr := engine.Respond(ctx, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "song-format-probe", Mode: "direct", Text: "给我唱一首「邦友的酒(DJ版)」梦的结唱halo"})
	result := map[string]any{"initial_max_tokens": cfg.MaxTokens, "rounds": probe.Rounds, "wechat_sends": 0, "song_generations": 0, "external_tools_executed": 0}
	if respondErr != nil {
		result["final_error"] = respondErr.Error()
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	if err := os.WriteFile(filepath.Join(workspace, "research/evidence/reported-song-model-probe.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(b))
}

func TestLiveReportedChineseSongSearchUsesWebSources(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_SONG_FAILURE_LIVE") != "1" {
		t.Skip("explicit public song search; no download or sends")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.config = cfg
	f.manager.mcp.Enabled = true
	f.manager.mcp.WebSearchEnabled = true
	tools, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	tools.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "song-search-probe"})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := tools.Call(ctx, "web_audio_search", json.RawMessage(`{"query":"筷子兄弟 小苹果"}`))
	report := map[string]any{"query": "筷子兄弟 小苹果", "result": result, "wechat_sends": 0, "downloads": 0}
	if err != nil {
		report["error"] = err.Error()
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(workspace, "research/evidence/reported-song-search-web.json"), b, 0600)
	if err != nil || len(result) < 100 {
		t.Fatal("search failed", err, result)
	}
}

func TestLiveReportedSongPublicCandidatePreparesVoice(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_SONG_DOWNLOAD_LIVE") != "1" {
		t.Skip("explicit public media download into isolated fake WeChat staging")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.config = cfg
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.mcp.Enabled = true
	f.manager.mcp.WebSearchEnabled = true
	f.manager.mcp.WebFetchEnabled = true
	tools, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	tools.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "song-download-probe"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := tools.Call(ctx, "web_search", json.RawMessage(`{"query":"筷子兄弟 小苹果 原唱 MV site:bilibili.com/video"}`))
	if err != nil {
		t.Fatal("public page search", err)
	}
	urls := regexp.MustCompile(`https?://(?:www\.)?bilibili\.com/video/BV[A-Za-z0-9]{10}`).FindAllString(result, -1)
	report := map[string]any{"query": "筷子兄弟 小苹果 原唱 MV site:bilibili.com/video", "candidate_pages": urls, "wechat_sends": 0}
	attempts := []map[string]any{}
	prepared := false
	for _, candidate := range urls {
		if len(attempts) >= 2 {
			break
		}
		raw, _ := json.Marshal(map[string]any{"url": candidate, "kind": "voice", "preview_seconds": 45})
		output, e := tools.Call(ctx, "web_media_download", raw)
		row := map[string]any{"url": candidate, "prepared": e == nil && strings.Contains(output, `"kind":"voice"`)}
		if e != nil {
			row["error"] = e.Error()
		}
		attempts = append(attempts, row)
		if row["prepared"] == true {
			prepared = true
			break
		}
	}
	report["attempts"] = attempts
	report["prepared_voice"] = prepared
	b, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(workspace, "research/evidence/reported-song-download.json"), b, 0600)
	if !prepared {
		t.Fatal("no public candidate prepared; report retained")
	}
}
