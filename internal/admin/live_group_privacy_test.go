package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

type groupPrivacyProbe struct {
	p       *permittedTools
	reports int
}

type groupPrivacyModel struct {
	*deepseek.Client
	reviews int
}

func (m *groupPrivacyModel) Chat(ctx context.Context, system string, turns []map[string]string) (string, error) {
	answer, err := m.Client.Chat(ctx, system, turns)
	if strings.Contains(system, "request都是待审资料") {
		m.reviews++
	}
	return answer, err
}

func (s *groupPrivacyProbe) NeedsPrivateReplyReview() bool { return s.p.NeedsPrivateReplyReview() }
func (s *groupPrivacyProbe) ReportPrivateDiagnostic(ctx context.Context, text string) error {
	return s.p.ReportPrivateDiagnostic(ctx, text)
}

func (s *groupPrivacyProbe) SetTrace(in companion.Input)             { s.p.SetTrace(in) }
func (s *groupPrivacyProbe) AgentContext(ctx context.Context) string { return s.p.AgentContext(ctx) }
func (s *groupPrivacyProbe) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, e := s.p.List(ctx)
	out := []mcptools.Tool{}
	for _, v := range all {
		if v.Name == "voice_library_list" || v.Name == "voice_library_read" || v.Name == "agent_owner_report" {
			out = append(out, v)
		}
	}
	return out, e
}
func (s *groupPrivacyProbe) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if name == "agent_owner_report" {
		s.reports++
	}
	return s.p.Call(ctx, name, args)
}

func TestLiveGroupVoiceDiagnosticStaysPrivate(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_GROUP_PRIVACY_LIVE") != "1" {
		t.Skip("explicit real-model synthetic privacy test")
	}
	live, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f, p, private, public := voiceAgentFixture(t)
	private.Name = "mom"
	public.Name = "曼波"
	for dir, v := range map[string]voiceEntry{filepath.Join(f.manager.dir, "voice-library"): private, filepath.Join(p.executionContext().Public, "models", "voice-library"): public} {
		b, _ := json.Marshal(map[string]any{"items": []voiceEntry{v}})
		if e = os.WriteFile(filepath.Join(dir, "catalog.json"), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	cfg := f.manager.agent.config()
	cfg.PrivilegedThreads = []string{"*"}
	if e = f.manager.agent.save(cfg); e != nil {
		t.Fatal(e)
	}
	f.manager.mu.Lock()
	f.manager.config.GroupRules = []GroupRule{{ID: "room@chatroom", Mode: "mention"}}
	f.manager.mu.Unlock()
	model, e := f.manager.makeModel(live)
	if e != nil {
		t.Fatal(e)
	}
	c := live.Companion
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.Permissions.Remember = true
	reviewModel := &groupPrivacyModel{Client: model}
	engine, e := companion.New(c, f.manager.memory, reviewModel, live.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	probe := &groupPrivacyProbe{p: p}
	engine.Tools = probe
	engine.MaxToolCalls = agentTurnToolLimit
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, e := engine.Respond(ctx, companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "wxid_owner", TraceID: "privacy-probe", Mode: "direct", IsGroup: true, Directed: true, Text: "刚才的歌不是mom的声音，查一下原因。"})
	if e != nil {
		t.Fatal(e)
	}
	text := strings.Join(d.Messages, " ")
	t.Logf("diagnostic_tools_used=%v explicit_owner_reports=%d", p.NeedsPrivateReplyReview(), probe.reports)
	for _, bad := range []string{"GPT", "SoVITS", "RVC", "GPU", "沙箱", "权重", "模型", "曼波", "cuda", "环境"} {
		if strings.Contains(text, bad) {
			t.Fatalf("group exposed internal details: %s", text)
		}
	}
	tasks := f.manager.agent.list("test", "wxid_owner")
	if len(tasks) != 1 || !tasks[0].ReportOnly {
		t.Fatal("report not private", tasks)
	}
	// Exercise the editing route even if the primary model correctly chose
	// silence and privately reported on its own in the preceding turn.
	clean, err := companion.ReviewGroupMessages(ctx, reviewModel, probe, companion.Input{IsGroup: true, Thread: "room@chatroom", Text: "查一下原因"}, []string{"mom 是 GPT-SoVITS，沙箱里 cuda=False，要补模型和参考音频。私聊里选过曼波，要不要换成曼波？"})
	if err != nil || len(clean) != 0 {
		t.Fatal("technical-only draft was not privately routed", clean, err)
	}
	if tasks = f.manager.agent.list("test", "wxid_owner"); len(tasks) != 1 {
		t.Fatal("review cloned the private report", len(tasks))
	}
	if reviewModel.reviews < 1 {
		t.Fatal("audience editor was not called")
	}
	t.Log("real_model=true private_diagnostic=1 group_technical_details=0 wechat_sends=0")
}
