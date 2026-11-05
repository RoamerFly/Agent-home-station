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
	"wechat-bot/internal/mcptools"
)

type librarySpeechNative struct {
	*liveResourceStaging
	account string
}

func (n *librarySpeechNative) Account() string { return n.account }

type librarySpeechProbe struct {
	p        *permittedTools
	calls    []string
	selected string
}

func (q *librarySpeechProbe) SetTrace(in companion.Input)                      { q.p.SetTrace(in) }
func (q *librarySpeechProbe) AgentContext(ctx context.Context) string          { return q.p.AgentContext(ctx) }
func (q *librarySpeechProbe) Media(id string) (companion.GeneratedMedia, bool) { return q.p.Media(id) }
func (q *librarySpeechProbe) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, err := q.p.List(ctx)
	out := []mcptools.Tool{}
	for _, v := range all {
		if strings.HasPrefix(v.Name, "voice_library_") || v.Name == "speech_synthesize" || v.Name == "project_read" || v.Name == "server_read" || v.Name == "tool_output_read" {
			out = append(out, v)
		}
	}
	return out, err
}
func (q *librarySpeechProbe) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	q.calls = append(q.calls, name)
	if name == "speech_synthesize" {
		var v struct {
			ID string `json:"voice_id"`
		}
		json.Unmarshal(args, &v)
		q.selected = v.ID
	}
	return q.p.Call(ctx, name, args)
}

// Real configured model + real private weights + mandatory CUDA sandbox;
// synthetic old history and fake upload transport. Never sends to WeChat.
func TestLiveModelUsesNamedLibrarySpeechDespiteStaleUnavailableHistory(t *testing.T) {
	file := os.Getenv("WECHAT_LIBRARY_SPEECH_LIVE_JSON")
	if file == "" {
		t.Skip("explicit local model/inference verification")
	}
	var in struct{ Control, Workspace, Account, Owner, Directory, VoiceID, Text, Report string }
	b, e := os.ReadFile(file)
	if e != nil || json.Unmarshal(b, &in) != nil || in.Control == "" || in.Workspace == "" || in.Owner == "" || in.VoiceID == "" {
		t.Fatal("invalid explicit private live input")
	}
	live, e := loadConfig(filepath.Join(in.Control, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("WECHAT_WORKSPACE_ROOT", "")
	f := setup(t)
	t.Setenv("WECHAT_WORKSPACE_ROOT", in.Workspace)
	native := &librarySpeechNative{liveResourceStaging: &liveResourceStaging{fakeNative: f.native}, account: in.Account}
	f.manager.native = native
	f.manager.workspaceDir = in.Directory
	f.manager.config = live
	f.manager.config.Enabled = false
	f.manager.config.PrivateEnabled = true
	f.manager.config.DirectIDs = []string{in.Owner}
	f.manager.config.Companion.Permissions.SendVoice = true
	settings := f.manager.agent.config()
	settings.Enabled = true
	settings.OwnerIDs = []string{in.Owner}
	settings.PrivilegedThreads = []string{"*"}
	settings.CrossConversation = true
	settings.Host.Mode = "full"
	if e = f.manager.agent.save(settings); e != nil {
		t.Fatal(e)
	}
	access, _, e := f.manager.toolAccess()
	if e != nil {
		t.Fatal(e)
	}
	model, e := f.manager.makeModel(live)
	if e != nil {
		t.Fatal(e)
	}
	c := live.Companion
	c.Enabled = true
	c.Permissions.Remember = true
	c.Permissions.SendVoice = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	input := companion.Input{Channel: "wechat", Account: in.Account, Thread: in.Owner, Sender: in.Owner, Mode: "direct", Text: "用mom的声线给我说一句“" + in.Text + "”", TraceID: "local-named-voice-no-send", MessageTime: time.Now().Format(time.RFC3339)}
	old := input
	old.Text = "用mom的声线说一句话"
	now := time.Now().Add(-time.Hour)
	if e = f.manager.memory.Observe(old, c, now); e != nil {
		t.Fatal(e)
	}
	if e = f.manager.memory.Accepted(old, "这台机器没有mom的合成环境，只有默认角色音色可用。", c, now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	engine, e := companion.New(c, f.manager.memory, model, live.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	probe := &librarySpeechProbe{p: access}
	engine.Tools = probe
	engine.MaxToolCalls = agentTurnToolLimit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	decision, err := engine.Respond(ctx, input)
	report := map[string]any{"decision": decision, "error": err != nil, "calls": probe.calls, "selected_voice_id": probe.selected, "uploads": native.uploads, "uploaded_bytes": native.bytes, "wechat_sends": 0}
	for _, op := range f.manager.agent.data.Operations {
		if op.Tool == "speech_synthesize" {
			var result map[string]any
			if json.Unmarshal([]byte(op.Result), &result) == nil {
				report["synthesis_result"] = result
			}
		}
	}
	if in.Report != "" {
		data, _ := json.MarshalIndent(report, "", "  ")
		if e = os.WriteFile(in.Report, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if err != nil || probe.selected != in.VoiceID || native.uploads < 1 || len(decision.MediaIDs) < 1 {
		t.Fatal("real model did not prepare requested voice", report)
	}
	t.Logf("real_model=true named_voice=%s prepared_voice=%d wechat_sends=0", probe.selected, len(decision.MediaIDs))
}
