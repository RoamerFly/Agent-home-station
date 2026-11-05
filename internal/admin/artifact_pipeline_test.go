package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

func artifactPipelineFixture(t *testing.T) (*webFixture, *permittedTools, *fakeTools, mediaAsset) {
	t.Helper()
	f, p := trustedAgent(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.mu.Unlock()
	settings := f.manager.agent.config()
	settings.Enabled = true
	settings.Host.Mode = "files"
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "synthetic-attachment", Text: "把这个做成 PDF"})
	asset, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: []byte("synthetic immutable input"), MIME: "text/plain", Filename: "source.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f, p, native, asset
}

func TestArtifactPipelineCopiesInputStagesOutputAndNeverSends(t *testing.T) {
	_, p, native, asset := artifactPipelineFixture(t)
	got, err := p.MaterializeArtifactForTask(context.Background(), asset.ID, "synthetic-attachment", "")
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(got.InputPath)
	if err != nil || string(input) != "synthetic immutable input" || !strings.Contains(got.OutputDir, "tasks") {
		t.Fatal("materialized input wrong", string(input), got, err)
	}
	// The materialized input is a copy: changing it cannot mutate the retained
	// asset; generated content is only accepted from the dedicated output dir.
	if err = os.WriteFile(got.InputPath, []byte("edited copy"), 0600); err != nil {
		t.Fatal(err)
	}
	retained, err := p.manager.cachedAssetData(asset)
	if err != nil || string(retained) != "synthetic immutable input" {
		t.Fatal("source asset mutated", string(retained), err)
	}
	if err = os.WriteFile(filepath.Join(got.OutputDir, "result.pdf"), []byte("synthetic pdf product"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := p.PublishArtifactForTask(context.Background(), ArtifactPublishRequest{Path: "result.pdf"})
	if err != nil || !strings.Contains(result, `"staged_only":true`) || !strings.Contains(result, `"kind":"file"`) {
		t.Fatal("publish failed", result, err)
	}
	if native.submits.Load() != 0 {
		t.Fatal("artifact publish must not send WeChat", native.submits.Load())
	}
}

func TestArtifactPipelineRejectsCrossRequesterAndUnsafeOutput(t *testing.T) {
	_, p, _, asset := artifactPipelineFixture(t)
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "someone-else", TraceID: "other-request"})
	if _, err := p.MaterializeArtifactForTask(context.Background(), asset.ID, "", ""); err == nil {
		t.Fatal("other group/direct participant read source attachment")
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "synthetic-attachment"})
	materialized, err := p.MaterializeArtifactForTask(context.Background(), asset.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.PublishArtifactForTask(context.Background(), ArtifactPublishRequest{Path: "../source.txt"}); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err = os.Symlink(materialized.InputPath, filepath.Join(materialized.OutputDir, "linked.pdf")); err == nil {
		if _, err = p.PublishArtifactForTask(context.Background(), ArtifactPublishRequest{Path: "linked.pdf"}); err == nil {
			t.Fatal("symlink output accepted")
		}
	}
}

func TestArtifactToolsAreDiscoverableAndUseNoLiveSend(t *testing.T) {
	_, p, native, asset := artifactPipelineFixture(t)
	names := map[string]bool{}
	for _, tool := range p.agentTools() {
		names[tool.Name] = true
	}
	if !names["artifact_materialize"] || !names["artifact_publish"] {
		t.Fatal("artifact host tools not discoverable", names)
	}
	raw, _ := json.Marshal(map[string]string{"asset_id": asset.ID, "source_message_id": "synthetic-attachment"})
	result, err := p.callAgent(context.Background(), "artifact_materialize", raw)
	if err != nil || !strings.Contains(result, `"original_immutable":true`) || native.submits.Load() != 0 {
		t.Fatal("tool materialization wrong", result, err, native.submits.Load())
	}
}

func TestArtifactPipelineRetainsAttachmentAcrossFollowupAndStagesOnlyPDF(t *testing.T) {
	_, p, native, source := artifactPipelineFixture(t)
	// This simulates the normal chat shape: a user first sends an attachment,
	// then, in a later message, asks for a converted result.  The follow-up is
	// deliberately a different trace so the pipeline cannot accidentally depend
	// on in-memory state from the receipt event.
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "synthetic-convert-request", Text: "用 Word 重建并只发 PDF"})
	materialized, err := p.MaterializeArtifactForTask(context.Background(), source.ID, "synthetic-attachment", "")
	if err != nil {
		t.Fatal("later request lost retained original", err)
	}
	if err = os.WriteFile(filepath.Join(materialized.OutputDir, "rebuilt.docx"), []byte("synthetic editable docx"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(materialized.OutputDir, "rebuilt.pdf"), []byte("%PDF-synthetic product"), 0600); err != nil {
		t.Fatal(err)
	}
	// The requested delivery is PDF only.  DOCX remains the task-local editable
	// source and is not registered or exposed as a pending outbound medium.
	result, err := p.PublishArtifactForTask(context.Background(), ArtifactPublishRequest{Path: "rebuilt.pdf", Title: "rebuilt.pdf", TaskID: materialized.TaskID})
	if err != nil || !strings.Contains(result, `"title":"rebuilt.pdf"`) || strings.Contains(result, "rebuilt.docx") {
		t.Fatal("PDF-only staging failed", result, err)
	}
	if native.submits.Load() != 0 {
		t.Fatal("normal conversion preparation unexpectedly sent WeChat", native.submits.Load())
	}
	items, err := p.manager.loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	var pdfFound, docxFound bool
	for _, item := range items {
		pdfFound = pdfFound || item.Filename == "rebuilt.pdf" && item.FirstRequest != nil && item.FirstRequest.TraceID == "synthetic-convert-request"
		docxFound = docxFound || item.Filename == "rebuilt.docx"
	}
	if !pdfFound || docxFound {
		t.Fatal("wrong artifact retention", items)
	}
}

func TestArtifactPipelineCannotCrossConversationOrTask(t *testing.T) {
	_, p, _, asset := artifactPipelineFixture(t)
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "other@chatroom", Sender: "wxid_owner", TraceID: "other-thread"})
	if _, err := p.MaterializeArtifactForTask(context.Background(), asset.ID, "synthetic-attachment", ""); err == nil {
		t.Fatal("attachment crossed conversation boundary")
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "new-request", TaskID: "task-a"})
	if _, err := p.MaterializeArtifactForTask(context.Background(), asset.ID, "", "task-b"); err == nil {
		t.Fatal("caller selected another task id")
	}
}

func TestBuiltInWechatFileTransportDoesNotDependOnDocumentPlugin(t *testing.T) {
	_, p, native, _ := artifactPipelineFixture(t)
	if tools := p.documentTools(); len(tools) != 0 {
		t.Fatal("fixture unexpectedly has the optional document plugin", tools)
	}
	communication := map[string]bool{}
	for _, tool := range p.wechatCommunicationTools() {
		communication[tool.Name] = true
	}
	// File receipt is available to an authorized current conversation.  Sending
	// a prepared current-reply attachment stays in the core final-reply path;
	// the owner-only cross-conversation transport is separately discoverable
	// only when cross-conversation permission is granted.
	if !communication["wechat_file_read"] || !communication["wechat_delivery_status"] {
		t.Fatal("core file transport hidden by absent processor", communication)
	}
	registered := map[string]bool{}
	for _, capability := range p.capabilities() {
		registered[capability.id] = true
	}
	if !registered["wechat-communication"] || !registered["workspace-agent"] || registered["workspace-agent-wechat"] {
		t.Fatal("built-in communication capability grouping invalid", registered)
	}
	result, err := p.stageOutputWithAsset(context.Background(), plugins.Output{Media: []plugins.MediaResult{{Kind: "file", Data: []byte("synthetic ordinary file"), MIMEType: "text/plain", Filename: "ordinary.txt"}}}, nil)
	if err != nil || !strings.Contains(result, `"kind":"file"`) {
		t.Fatal("built-in file staging failed", result, err)
	}
	if native.submits.Load() != 0 {
		t.Fatal("staging a normal file submitted a WeChat send", native.submits.Load())
	}
}

func TestPluginCenterListsUninstallableCoreWechatFileTransport(t *testing.T) {
	f := setup(t)
	f.login()
	code, body, _ := f.request("GET", "/api/plugin-packages", nil, true, true)
	if code != 200 || !strings.Contains(string(body), "微信文件收发（内置）") || !strings.Contains(string(body), `"uninstallable":false`) || !strings.Contains(string(body), "已授权会话") {
		t.Fatalf("plugin center did not describe core transport: %d %s", code, body)
	}
}
