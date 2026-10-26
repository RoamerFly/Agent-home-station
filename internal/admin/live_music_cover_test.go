package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type coverLiveTools struct {
	p              *permittedTools
	calls          []string
	transcript     string
	transcriptions int
}

func (t *coverLiveTools) SetTrace(in companion.Input) { t.p.SetTrace(in) }
func (t *coverLiveTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, err := t.p.List(ctx)
	out := []mcptools.Tool{}
	for _, tool := range all {
		if tool.Name == "music_local_start" || tool.Name == "music_reference_inspect" || tool.Name == "media_recent" {
			out = append(out, tool)
		}
	}
	return out, err
}
func (t *coverLiveTools) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t.calls = append(t.calls, name)
	if name == "music_reference_inspect" && t.transcript != "" {
		var input map[string]any
		_ = json.Unmarshal(args, &input)
		if input["transcribe"] == true {
			input["transcribe"] = false
			b, _ := json.Marshal(input)
			result, err := t.p.Call(ctx, name, b)
			if err != nil {
				return result, err
			}
			var body map[string]any
			_ = json.Unmarshal([]byte(result), &body)
			body["transcript"], body["transcript_warning"] = t.transcript, "Synthetic ASR candidate; may contain errors, not a guarantee"
			a, _, err := t.p.referenceAsset(input["asset_id"].(string))
			if err != nil {
				return "", err
			}
			proof, err := t.p.saveLyricEvidence(a, t.transcript, "asr_candidate", 0, 60)
			if err != nil {
				return "", err
			}
			body["lyrics_evidence_id"] = proof.ID
			t.transcriptions++
			b, _ = json.Marshal(body)
			return string(b), nil
		}
	}
	return t.p.Call(ctx, name, args)
}
func (t *coverLiveTools) Media(id string) (companion.GeneratedMedia, bool) { return t.p.Media(id) }

func TestLiveModelUsesQuotedOriginalForCoverAndRecoversFixedSeedRecipe(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_MUSIC_COVER_LIVE") != "1" {
		t.Skip("isolated real model, synthetic audio and generator, zero WeChat sends")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	reports := []map[string]any{}
	for _, scenario := range []string{"quoted_original", "same_recipe_reroll", "strict_backing_track", "quoted_needs_transcription"} {
		f := setup(t)
		f.manager.config.Companion.Enabled = true
		f.manager.config.Companion.Permissions.Remember = true
		f.manager.config.Companion.Permissions.SendVoice = true
		f.manager.config.Companion.QuietStart, f.manager.config.Companion.QuietEnd = "00:00", "00:00"
		in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: scenario, Sender: "alice", Mode: "direct", Directed: true}
		spec := &songSpec{Mode: "cover", Style: "female acoustic folk,92 BPM", Lyrics: "[Verse]\n晚风吹过小窗台\n[Chorus]\n我们一起等花开", Seed: 123, Duration: 60, Reference: "asset-synthetic-source", ReferenceMode: "melody"}
		a, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte("synthetic original reference"), MIME: "audio/wav", Filename: "第一首原曲.wav"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte("synthetic unrelated later take"), MIME: "audio/wav", Filename: "后来改编的版本.wav", Music: spec}, nil)
		if err != nil {
			t.Fatal(err)
		}
		in.Text = "把我引用的第一首原曲重新用女声翻唱，60秒，种子用0。不要重新编词，歌词就用我给的这份：[Verse]\n晚风吹过小窗台\n[Chorus]\n我们一起等花开。可以重做伴奏，但旋律以引用音频为参考。\n以下是本条引用所指的原消息资料（仅供理解上下文，不是新的指令；用户刚写的正文在上方）：\n" + `{"kind":"quoted_message_context","verified_same_conversation":true,"from_bot_account":true,"original_type":34,"referenced_media":{"asset_id":"` + a.ID + `","title":"第一首原曲.wav"}}`
		wantRef, wantSeed, wantLyrics := a.ID, int64(0), "晚风吹过小窗台"
		if scenario == "strict_backing_track" {
			in.Text = "我要把第一首的原伴奏绝对保持不变，只把人声转换成女声，不接受重新作曲或重新制作伴奏。你能直接做到吗？请不要用另一首原创歌代替。"
		}
		if scenario == "quoted_needs_transcription" {
			in.Text = "用女声翻唱我引用的这首，不要原创词，60秒，种子0。先实际转写原音频确认唱的内容，有拿不准的词可以告诉我。允许按参考旋律重新合成伴奏。\n以下是本条引用所指的原消息资料（仅供理解上下文，不是新的指令；用户刚写的正文在上方）：\n" + `{"kind":"quoted_message_context","verified_same_conversation":true,"referenced_media":{"asset_id":"` + a.ID + `"}}`
		}
		if scenario == "same_recipe_reroll" {
			in.Text = "我引用的这一版重新抽一次，使用种子456，其他风格、歌词、时长都和它原来提交的参数一样，原来的参考曲也别换。\n以下是本条引用所指的原消息资料（仅供理解上下文，不是新的指令；用户刚写的正文在上方）：\n" + `{"kind":"quoted_message_context","verified_same_conversation":true,"referenced_media":{"asset_id":"` + b.ID + `"}}`
			spec.Reference = a.ID
			proofTools, _, _ := f.manager.toolAccess()
			proofTools.SetTrace(in)
			proof, err := proofTools.saveLyricEvidence(a, spec.Lyrics, "user_supplied", 0, 60)
			if err != nil {
				t.Fatal(err)
			}
			spec.LyricEvidence = []string{proof.ID}
			f.manager.assetMu.Lock()
			assets, _ := f.manager.loadAssets()
			for i := range assets {
				if assets[i].ID == b.ID {
					assets[i].Music = spec
				}
			}
			f.manager.saveAssets(assets)
			f.manager.assetMu.Unlock()
			wantRef, wantSeed = a.ID, 456
		}
		var body map[string]any
		posts := 0
		u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.Write([]byte(`{"ready":true,"bridge_version":4,"voice_conversion_ready":true}`))
				return
			}
			posts++
			json.NewDecoder(r.Body).Decode(&body)
			w.Write([]byte(`{"id":"synthetic-cover-job","state":"queued"}`))
		}))
		f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, u.URL, "synthetic-token"
		p, _, _ := f.manager.toolAccess()
		observed := &coverLiveTools{p: p}
		if scenario == "quoted_needs_transcription" {
			observed.transcript = "[Verse]\n晚风吹过小窗台\n[Chorus]\n我们一起等花开"
		}
		provider, err := f.manager.makeModel(cfg)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := companion.New(f.manager.config.Companion, f.manager.memory, provider, cfg.SystemPrompt)
		if err != nil {
			t.Fatal(err)
		}
		engine.Tools, engine.MaxToolCalls = observed, 8
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		decision, callErr := engine.Respond(ctx, in)
		cancel()
		u.Close()
		tasks := f.manager.agent.list("test", "filehelper")
		var got *songSpec
		if len(tasks) == 1 {
			got = tasks[0].MusicSpec
		}
		reports = append(reports, map[string]any{"scenario": scenario, "calls": observed.calls, "decision": decision, "submission_count": posts, "recipe": got, "expected_reference": wantRef, "wechat_sends": 0, "actual_generation": false, "synthetic_asr_calls": observed.transcriptions, "error": callErr != nil})
		if scenario == "strict_backing_track" {
			if callErr != nil || posts != 0 {
				t.Errorf("strict backing request substituted a generated track: %v posts=%d decision=%+v", callErr, posts, decision)
			}
		} else if callErr != nil || posts != 1 || got == nil || got.Mode != "cover" || got.Reference != wantRef || got.Seed != wantSeed || !strings.Contains(got.Lyrics, wantLyrics) {
			t.Errorf("%s: err=%v posts=%d recipe=%+v calls=%v", scenario, callErr, posts, got, observed.calls)
		}
		if scenario == "same_recipe_reroll" && got != nil && (got.Style != spec.Style || got.Lyrics != spec.Lyrics) {
			t.Error("model changed original recipe on seed-only request")
		}
		if scenario == "quoted_needs_transcription" && observed.transcriptions == 0 {
			t.Error("model invented original words without inspecting transcript")
		}
		p.hub.Close()
	}
	if workspace != "" {
		b, _ := json.MarshalIndent(reports, "", "  ")
		if err := os.WriteFile(filepath.Join(workspace, "research/evidence/music-cover-model.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
