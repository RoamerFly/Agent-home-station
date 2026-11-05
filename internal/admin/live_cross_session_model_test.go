package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

// Only these four real tool handlers are exposed. Every native operation is
// backed by forwardNative, never by the live native client's credentials.
type crossSessionModelTools struct {
	p       *permittedTools
	native  *forwardNative
	manager *Manager
	calls   []string
	parts   []bot.MediaSeed
	receipt bool
}

func (t *crossSessionModelTools) SetTrace(in companion.Input) { t.p.SetTrace(in) }
func (t *crossSessionModelTools) AgentContext(ctx context.Context) string {
	return t.p.AgentContext(ctx)
}
func (t *crossSessionModelTools) AutonomousActions() bool { return t.p.AutonomousActions() }
func crossModelTool(name string) bool {
	return name == "agent_global_search" || name == "wechat_conversations" || name == "wechat_send_media" || name == "wechat_operation_status"
}
func (t *crossSessionModelTools) List(context.Context) ([]mcptools.Tool, error) {
	tools := []mcptools.Tool{}
	for _, tool := range t.p.agentTools() {
		if crossModelTool(tool.Name) {
			tools = append(tools, tool)
		}
	}
	return tools, nil
}
func (t *crossSessionModelTools) Call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if !crossModelTool(name) {
		return "", fmt.Errorf("isolated model fixture: tool unavailable")
	}
	t.calls = append(t.calls, name)
	if name == "wechat_operation_status" && t.native.submits.Load() == 0 {
		// Simulate the normal worker draining its durable queue. Submit is fake;
		// the real receipt projection and batch reconciliation still run.
		t.parts = t.manager.takeMediaSeeds()
		for _, part := range t.parts {
			if _, err := t.native.Submit(ctx, linuxnative.ToolSend{ThreadID: part.Input.Thread, Kind: part.Media.Kind, SourceID: part.SourceID, IdempotencyKey: part.Key}); err != nil {
				return "", err
			}
		}
	}
	result, err := t.p.Call(ctx, name, raw)
	if name == "wechat_operation_status" && err == nil {
		var receipt struct {
			State  string                   `json:"state"`
			Target string                   `json:"target_thread_id"`
			Source string                   `json:"source_thread_id"`
			Parts  []linuxnative.ToolStatus `json:"parts"`
		}
		if json.Unmarshal([]byte(result), &receipt) == nil && receipt.State == "accepted" && receipt.Target == t.p.trace.Thread && receipt.Source == "source@chatroom" && len(receipt.Parts) == 1 && receipt.Parts[0].ServerID != "" {
			t.receipt = true
		}
	}
	return result, err
}

func TestLiveModelChoosesLatestCrossSessionFileAndTarget(t *testing.T) {
	root, evidence := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_CROSS_SESSION_MODEL_LIVE") != "1" {
		t.Skip("explicit real-model evaluation with synthetic conversations and fake native")
	}
	// Fixture writes must never inherit the real deployment's workspace root.
	t.Setenv("WECHAT_WORKSPACE_ROOT", "")
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f, access, native := forwardFixture(t)
	c := cfg.Companion
	c.Enabled, c.Permissions.Remember, c.Permissions.SendFile = true, true, true
	c.QuietStart, c.QuietEnd = "00:00", "00:00"
	f.manager.config.Companion = c
	f.manager.config.GroupRules = []GroupRule{{ID: "target@chatroom", Name: "合成目标群", Mode: "mention"}}
	if err := f.manager.rememberNativeConversations(map[string]string{"source@chatroom": "合成资料群"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	want := ""
	for index, stamp := range []time.Time{now.Add(-2 * time.Hour).In(time.FixedZone("UTC+8", 8*3600)), now.Add(-time.Hour)} {
		input := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "synthetic-member", SenderName: "合成群友", TraceID: fmt.Sprint("file-", index), MessageTime: stamp.Format(time.RFC3339Nano), Text: fmt.Sprintf("[文件] 合成资料-%d.txt", index)}
		if err := f.manager.memory.Observe(input, c, now); err != nil {
			t.Fatal(err)
		}
		asset, err := f.manager.rememberAssetFormats(input, assetOriginal{Data: []byte(fmt.Sprint("synthetic-file-", index)), MIME: "text/plain", Filename: fmt.Sprintf("合成资料-%d.txt", index)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			want = asset.ID
		}
	}
	provider, err := f.manager.makeModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(c, f.manager.memory, provider, cfg.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	tools := &crossSessionModelTools{p: access, native: native, manager: f.manager}
	engine.Tools, engine.MaxToolCalls = tools, 0
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "target@chatroom", Sender: "wxid_owner", SenderName: "合成主人", TraceID: "cross-model-request", MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true, IsGroup: true, Text: "把合成资料群最新的文件发到这里，只发文件。发完核对一下。"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	decision, err := engine.Respond(ctx, input)
	parts := tools.parts
	matched := len(parts) == 1 && parts[0].Media.AssetID == want && parts[0].Input.Thread == input.Thread && parts[0].OriginThread == input.Thread && parts[0].SourceThread == "source@chatroom" && parts[0].Media.Kind == "file"
	bytesMatched := string(native.uploaded) == "synthetic-file-1"
	sourceAutoReply := permittedRecipient(f.manager.Config().Config, "source@chatroom")
	report := map[string]any{"scenario": "newest_original_file_across_sessions", "real_model": true, "synthetic_conversations": true, "fake_native_only": true, "source_auto_reply_enabled": sourceAutoReply, "real_wechat_sends": 0, "calls": tools.calls, "selected_expected_asset_and_target": matched, "original_bytes_match": bytesMatched, "fake_receipt_verified": tools.receipt, "fake_submissions": native.submits.Load(), "decision_action": decision.Action, "decision": decision, "error": err != nil}
	if evidence != "" {
		data, e := json.MarshalIndent(report, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(evidence, "cross-session-model.json"), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if err != nil || sourceAutoReply || decision.Action != "reply" || !matched || !bytesMatched || !tools.receipt || native.submits.Load() != 1 || len(decision.MediaIDs) != 0 {
		data, _ := json.Marshal(report)
		t.Fatalf("model cross-session selection: err=%v report=%s", err, data)
	}
}
