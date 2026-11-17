package admin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
)

// Opt-in acceptance harness: blank memory, isolated files, no native sends.
// Only the private image and request named by environment variables are inputs.
// The reference document is deliberately not a parameter.
type trialImageNative struct {
	*fakeTools
	msg linuxnative.Message
}

func (n *trialImageNative) Message(context.Context, string) (linuxnative.Message, error) {
	return n.msg, nil
}

type imageDocumentTrial struct {
	p     *permittedTools
	image []byte
	msg   linuxnative.Message
	log   *os.File
}

func (x *imageDocumentTrial) SetTrace(in companion.Input)             { x.p.SetTrace(in) }
func (x *imageDocumentTrial) AgentContext(ctx context.Context) string { return x.p.AgentContext(ctx) }
func (x *imageDocumentTrial) AutonomousActions() bool                 { return x.p.AutonomousActions() }
func (x *imageDocumentTrial) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, e := x.p.List(ctx)
	out := []mcptools.Tool{}
	for _, v := range all {
		if v.Name == "media_recent" || strings.HasPrefix(v.Name, "server_") || strings.HasPrefix(v.Name, "artifact_") || strings.HasPrefix(v.Name, "document_") {
			out = append(out, v)
		}
	}
	out = append(out, agentTool("inspect_original_image", "Observe the original attached image using the installed visual model. Request text, layout, handwriting or other visible details with prompt. No reference document is available.", `{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"],"additionalProperties":false}`))
	return out, e
}
func (x *imageDocumentTrial) Call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var result string
	var err error
	if name == "inspect_original_image" {
		var in struct {
			Prompt string `json:"prompt"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return "", fmt.Errorf("invalid arguments")
		}
		result, err = (mediaUnderstanding{manager: x.p.manager}).observeImages(ctx, x.msg, in.Prompt, []deepseek.Image{{Data: x.image, MIME: "image/jpeg"}})
	} else {
		allowed, _ := x.List(ctx)
		found := false
		for _, v := range allowed {
			if v.Name == name {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("tool not available in isolated acceptance")
		}
		result, err = x.p.Call(ctx, name, raw)
	}
	record := map[string]any{"at": time.Now(), "tool": name, "arguments": json.RawMessage(raw), "result": result}
	if err != nil {
		record["error"] = err.Error()
	}
	b, _ := json.Marshal(record)
	x.log.Write(append(b, '\n'))
	x.log.Sync()
	return result, err
}
func TestLiveImageOnlyDocumentTrial(t *testing.T) {
	liveDir, imagePath, requestPath, out := os.Getenv("DOCUMENT_TRIAL_ADMIN"), os.Getenv("DOCUMENT_TRIAL_IMAGE"), os.Getenv("DOCUMENT_TRIAL_REQUEST"), os.Getenv("DOCUMENT_TRIAL_OUTPUT")
	if liveDir == "" || imagePath == "" || requestPath == "" || out == "" {
		t.Skip("explicit private image-only acceptance")
	}
	t.Setenv("WECHAT_WORKSPACE_ROOT", "")
	live, e := loadConfig(liveDir)
	if e != nil {
		t.Fatal(e)
	}
	if os.Getenv("DOCUMENT_TRIAL_THINKING") == "1" {
		live.ThinkingEnabled, live.ReasoningEffort, live.MaxTokens = true, "high", 8192
	}
	f, p := trustedAgent(t)
	n := &fakeTools{fakeNative: f.native}
	f.manager.native = n
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	defer f.manager.processes.Close()
	cfg := f.manager.agent.config()
	cfg.Enabled = true
	cfg.Host.Mode = "full"
	raw, e := os.ReadFile(os.Getenv("WECHAT_WINDOWS_TEST_CONFIG"))
	if e != nil {
		t.Fatal(e)
	}
	var win agenthost.WindowsConfig
	if json.Unmarshal(raw, &win) != nil {
		t.Fatal("invalid broker")
	}
	cfg.Host.Windows = &win
	if e = f.manager.agent.save(cfg); e != nil {
		t.Fatal(e)
	}
	store := installProjectPluginFixture(t, p)
	installDocumentWorkshopFixture(t, p)
	// Copy only the declared visual-service configuration, never memory/history.
	original, e := pluginhost.OpenStore(filepath.Join(liveDir, "plugin-host"), "desktop")
	if e != nil {
		t.Fatal(e)
	}
	defer original.Close()
	vc, e := original.ConfigurationForExecution(t.Context(), "media-understanding")
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := store.Prepare(t.Context(), "../../plugins/media-understanding")
	if e != nil {
		t.Fatal(e)
	}
	ins, e := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if e != nil {
		t.Fatal(e)
	}
	schema, e := store.ConfigSchema(t.Context(), "media-understanding")
	if e != nil {
		t.Fatal(e)
	}
	update := pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{}, Secrets: map[string]string{}}
	for k, v := range vc.Values {
		if schema.Properties[k].Secret {
			var s string
			json.Unmarshal(v, &s)
			update.Secrets[k] = s
		} else {
			update.Values[k] = v
		}
	}
	if _, e = store.SaveConfiguration(t.Context(), "media-understanding", update); e != nil {
		t.Fatal(e)
	}
	if _, e = store.SetEnabled(t.Context(), "media-understanding", ins.Revision, true); e != nil {
		t.Fatal(e)
	}
	f.manager.config.Enabled = true
	f.manager.config.Behavior.ReadImages = true
	f.manager.config.Companion.Enabled = true
	image, e := os.ReadFile(imagePath)
	if e != nil {
		t.Fatal(e)
	}
	request, e := os.ReadFile(requestPath)
	if e != nil {
		t.Fatal(e)
	}
	msg := linuxnative.Message{ID: "image-only-original", AccountID: "test", ThreadID: "wxid_owner", SenderID: "wxid_owner", ServerID: "image-only-source", Type: 3, Direction: "incoming", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	f.manager.native = &trialImageNative{fakeTools: n, msg: msg}
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "image-only-request", TaskID: "image-only-document", Mode: "direct", Directed: true, Text: string(request)}
	input.MessageTime = msg.CreatedAt
	input.OriginalText = &input.Text
	input.Source = &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: input.TraceID, ServerID: "image-only-request-server", Account: input.Account, Thread: input.Thread, Sender: input.Sender, MessageType: 1, OccurredAt: msg.CreatedAt, Revision: 1}
	if e = f.manager.agent.task(agentTask{ID: input.TaskID, Account: input.Account, Thread: input.Thread, Sender: input.Sender, State: "running", OwnerRequested: true, RequestedText: input.Text}); e != nil {
		t.Fatal(e)
	}
	originalText := ""
	imageInput := companion.Input{Channel: "wechat", Account: "test", Thread: msg.ThreadID, Sender: msg.SenderID, TraceID: msg.ID, MessageTime: msg.CreatedAt, OriginalText: &originalText, Text: "[图片]", Source: &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: msg.ID, ServerID: msg.ServerID, Account: "test", Thread: msg.ThreadID, Sender: msg.SenderID, MessageType: 3, OccurredAt: msg.CreatedAt, Revision: 1}}
	if e = f.manager.memory.Observe(imageInput, f.manager.config.Companion, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e = f.manager.memory.Observe(input, f.manager.config.Companion, time.Now()); e != nil {
		t.Fatal(e)
	}
	p.SetTrace(input)
	if _, e = f.manager.rememberAssetFormats(imageInput, assetOriginal{Data: image, MIME: "image/jpeg", Filename: "original.jpg"}, nil); e != nil {
		t.Fatal(e)
	}
	model, e := f.manager.makeModel(live)
	if e != nil {
		t.Fatal(e)
	}
	c := live.Companion
	c.Enabled = true
	c.Permissions.Remember = true
	c.Permissions.SendFile = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	engine, e := companion.New(c, f.manager.memory, model, live.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	os.MkdirAll(out, 0700)
	log, e := os.Create(filepath.Join(out, "tool-calls.private.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	engine.Tools = &imageDocumentTrial{p: p, image: image, msg: msg, log: log}
	engine.MaxToolCalls = 60
	// Save generated files before fixture cleanup, including failed attempts.
	defer func() {
		root := p.executionContext().Conversation
		outputs := map[string]bool{}
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			target := filepath.Join(out, "workspace", rel)
			os.MkdirAll(filepath.Dir(target), 0700)
			b, e := os.ReadFile(path)
			if e == nil {
				os.WriteFile(target, b, 0600)
				if strings.Contains(filepath.ToSlash(rel), "/output/") {
					switch strings.ToLower(filepath.Ext(path)) {
					case ".pdf":
						outputs[".pdf"] = outputs[".pdf"] || strings.HasPrefix(string(b), "%PDF-")
					case ".docx":
						if z, err := zip.OpenReader(path); err == nil {
							for _, member := range z.File {
								if member.Name == "word/document.xml" {
									outputs[".docx"] = true
								}
							}
							z.Close()
						}
					}
				}
			}
			return nil
		})
		if !outputs[".docx"] || !outputs[".pdf"] {
			t.Error("acceptance did not produce both DOCX and PDF; see private report")
		}
		if n.submits.Load() != 0 {
			t.Error("isolated acceptance unexpectedly submitted a native message")
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	decision, e := engine.Respond(ctx, input)
	report := map[string]any{"decision": decision, "real_model": true, "real_windows_tools": true, "history_supplied": false, "reference_supplied": false, "native_sends": n.submits.Load(), "visual_acceptance": "requires_independent_review", "transport": "isolated_native_fixture"}
	report["model_profile"] = map[string]any{"model": live.Model, "thinking": live.ThinkingEnabled, "reasoning_effort": live.ReasoningEffort, "max_tokens": live.MaxTokens}
	if e != nil {
		report["error"] = e.Error()
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(out, "report.private.json"), b, 0600)
	if e != nil {
		t.Fatal(e)
	}
}
