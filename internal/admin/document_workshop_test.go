package admin

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
)

func installDocumentWorkshopFixture(t *testing.T, p *permittedTools) pluginhost.InstalledPackage {
	t.Helper()
	store, err := p.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), "../../plugins/document-workshop")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	installed, err = store.SetEnabled(t.Context(), documentWorkshopPluginID, installed.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

func TestDocumentWorkshopDiscoverabilitySeparatesReadFromOutput(t *testing.T) {
	_, p, _, _ := artifactPipelineFixture(t)
	installDocumentWorkshopFixture(t, p)
	names := map[string]bool{}
	for _, tool := range p.agentTools() {
		names[tool.Name] = true
	}
	for _, want := range []string{"document_inspect", "document_verify", "document_reconstruct", "document_export_pdf"} {
		if !names[want] {
			t.Fatalf("document tool missing with file delivery enabled: %s (%v)", want, names)
		}
	}
	p.manager.mu.Lock()
	p.manager.config.Companion.Permissions.SendFile = false
	p.manager.mu.Unlock()
	names = map[string]bool{}
	for _, tool := range p.agentTools() {
		names[tool.Name] = true
	}
	if !names["document_inspect"] || !names["document_verify"] || names["document_reconstruct"] || names["document_export_pdf"] {
		t.Fatalf("read/write document discovery not separated: %v", names)
	}
}

func TestDocumentWorkshopPolicyBindsOnlyHostMaterializedPaths(t *testing.T) {
	_, p, _, asset := artifactPipelineFixture(t)
	installed := installDocumentWorkshopFixture(t, p)
	raw, _ := json.Marshal(map[string]any{"artifact_id": asset.ID, "source_message_id": "synthetic-attachment", "include_layout": true})
	policy, arguments, err := p.newDocumentWorkshopPolicy(context.Background(), "document.inspect", raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(arguments), policy.materialized[0].InputPath) || !strings.Contains(string(arguments), `"artifact"`) {
		t.Fatalf("model-visible arguments leaked host path or lost artifact binding: %s", arguments)
	}
	cap := pluginhost.Capability{ID: "document.inspect", Permissions: []string{"artifact.read"}}
	job := pluginhost.Job{Plugin: documentWorkshopPluginID, Tool: cap.ID, Version: installed.Package.Version, PackageDigest: installed.Digest, ConfigRevision: "0", Context: policy.invocation}
	metadata, err := policy.CallMetadata(context.Background(), job, cap)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Output string `json:"output_dir"`
		Inputs []struct {
			Path string
			ID   string `json:"artifact_id"`
		}
	}
	if err := json.Unmarshal(metadata["wechat/document"], &doc); err != nil || len(doc.Inputs) != 1 || doc.Output != policy.materialized[0].OutputDir || doc.Inputs[0].ID != asset.ID || doc.Inputs[0].Path != policy.materialized[0].InputPath {
		t.Fatalf("document metadata lost task-scoped binding: %+v (%v)", doc, err)
	}
}

func TestDocumentWorkshopEvidenceRedactsHostPaths(t *testing.T) {
	clean := sanitizeDocumentEvidence(map[string]any{
		"page_count":   2,
		"text":         "合成文档正文",
		"ocr":          map[string]any{"available": true, "text": "识别内容"},
		"runtime_root": `E:\private\runtime`,
		"text_path":    `E:\private\task\page.txt`,
		"render_pages": []any{`E:\private\task\page-1.png`},
		"doctor": map[string]any{
			"executable_paths": map[string]any{"pdftotext": `E:\private\runtime\pdftotext.exe`},
			"checks":           []any{map[string]any{"name": "pdf_text_extract", "status": "ok"}},
		},
	})
	raw, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, `E:\private`) || strings.Contains(text, "text_path") || strings.Contains(text, "render_pages") || strings.Contains(text, "executable_paths") {
		t.Fatalf("host path evidence leaked: %s", text)
	}
	if !strings.Contains(text, "合成文档正文") || !strings.Contains(text, "识别内容") || !strings.Contains(text, "page_count") {
		t.Fatalf("user-meaningful evidence lost: %s", text)
	}
}

// This opt-in test crosses the real installed package -> Go controller -> MCP
// -> Windows LPAC boundary. It uses only a fake native client and synthetic
// content; files are staged but never submitted to WeChat.
func TestDocumentWorkshopNormalLPACArtifactFlow(t *testing.T) {
	if os.Getenv("WECHAT_DOCUMENT_LPAC_TEST") != "1" {
		t.Skip("set WECHAT_DOCUMENT_LPAC_TEST=1 for Windows document integration")
	}
	if runtime.GOOS != "windows" {
		t.Skip("Windows LPAC document test")
	}
	python, err := filepath.Abs("../../dist/windows-native/runtimes/windows-python-cli/python.exe")
	if err != nil {
		t.Fatal(err)
	}
	documentRuntime, err := filepath.Abs("../../dist/windows-native/runtimes/document-workshop")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{python, documentRuntime, helper} {
		if _, err = os.Stat(item); err != nil {
			t.Fatalf("project-local runtime unavailable: %s: %v", item, err)
		}
	}
	options, _ := json.Marshal(map[string]any{
		"python": python, "pythonw": filepath.Join(filepath.Dir(python), "pythonw.exe"), "windows_helper": helper,
		"runtime_roots": []string{filepath.Dir(python)}, "document_runtime_root": documentRuntime,
	})
	t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", string(options))
	_, p, native, asset := artifactPipelineFixture(t)
	installDocumentWorkshopFixture(t, p)
	layout := map[string]any{"blocks": []any{
		map[string]any{"kind": "heading", "level": 1, "text": "合成文档验收"},
		map[string]any{"kind": "paragraph", "text": "此处验证真实 MCP、LPAC、可编辑 DOCX 和 PDF 投递准备。"},
		map[string]any{"kind": "table", "rows": []any{[]any{"项目", "结论"}, []any{"PDF", "已核验"}}},
	}}
	request, _ := json.Marshal(map[string]any{"artifact_id": asset.ID, "source_message_id": "synthetic-attachment", "layout": layout, "title": "合成文档验收", "title_font_size_pt": 22, "filename": "acceptance.docx"})
	raw, err := p.callAgent(context.Background(), "document_reconstruct", request)
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		State  string `json:"state"`
		Result struct {
			Outputs []struct {
				AssetID         string `json:"asset_id"`
				DeliveryMediaID string `json:"delivery_media_id"`
			} `json:"outputs"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(raw), &first) != nil || first.State != "completed" || len(first.Result.Outputs) != 1 || first.Result.Outputs[0].AssetID == "" || first.Result.Outputs[0].DeliveryMediaID == "" {
		t.Fatalf("DOCX normal flow did not yield a staged durable asset: %s", raw)
	}
	request, _ = json.Marshal(map[string]any{"artifact_id": first.Result.Outputs[0].AssetID, "filename": "acceptance.pdf"})
	raw, err = p.callAgent(context.Background(), "document_export_pdf", request)
	if err != nil {
		t.Fatal(err)
	}
	var second struct {
		State  string `json:"state"`
		Result struct {
			Evidence map[string]any `json:"evidence"`
			Outputs  []struct {
				AssetID         string `json:"asset_id"`
				DeliveryMediaID string `json:"delivery_media_id"`
			} `json:"outputs"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(raw), &second) != nil || second.State != "completed" || len(second.Result.Outputs) != 1 || second.Result.Outputs[0].AssetID == "" || second.Result.Outputs[0].DeliveryMediaID == "" {
		t.Fatalf("PDF normal flow did not yield a staged durable asset: %s", raw)
	}
	evidence, _ := json.Marshal(second.Result.Evidence)
	if strings.Contains(string(evidence), `:\\`) || strings.Contains(string(evidence), "output_pdf") || strings.Contains(string(evidence), "raster_pages") {
		t.Fatalf("PDF output leaked host paths: %s", evidence)
	}
	if native.submits.Load() != 0 {
		t.Fatalf("document preparation submitted WeChat: %d", native.submits.Load())
	}
}

// documentDeliveryNative is a strictly local WeChat transport: it records the
// exact bytes selected by the core delivery queue and returns a synthetic
// accepted receipt.  It deliberately has no network implementation.
type documentDeliveryNative struct {
	*fakeTools
	mu      sync.Mutex
	uploads [][]byte
	sends   []linuxnative.ToolSend
}

func (n *documentDeliveryNative) Upload(_ context.Context, name, mime string, r io.Reader, size int64) (linuxnative.UploadSource, error) {
	body, err := io.ReadAll(r)
	if err != nil || int64(len(body)) != size {
		return linuxnative.UploadSource{}, io.ErrUnexpectedEOF
	}
	n.mu.Lock()
	n.uploads = append(n.uploads, append([]byte(nil), body...))
	n.mu.Unlock()
	return linuxnative.UploadSource{ID: "document-pdf-source", Filename: name, ContentType: mime, ByteLength: size}, nil
}

func (n *documentDeliveryNative) Submit(_ context.Context, send linuxnative.ToolSend) (linuxnative.ToolStatus, error) {
	n.mu.Lock()
	n.sends = append(n.sends, send)
	n.mu.Unlock()
	n.submits.Add(1)
	return linuxnative.ToolStatus{ID: "document-pdf-send", State: "queued", Kind: send.Kind, ThreadID: send.ThreadID, SourceID: send.SourceID}, nil
}

func (n *documentDeliveryNative) LookupSend(ctx context.Context, _ string) (linuxnative.ToolStatus, error) {
	return n.ToolSendStatus(ctx, "")
}

func (n *documentDeliveryNative) ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.sends) == 0 {
		return linuxnative.ToolStatus{}, linuxnative.ErrToolNotFound
	}
	send := n.sends[len(n.sends)-1]
	return linuxnative.ToolStatus{ID: "document-pdf-send", State: "accepted", Kind: send.Kind, ThreadID: send.ThreadID, SourceID: send.SourceID, ServerID: "synthetic-document-pdf"}, nil
}

// TestDocumentWorkshopPDFOnlyDeliveryUsesCoreQueueAndReceipt covers the step
// after a document processor has produced both an editable DOCX and a PDF. It
// selects only the PDF asset, then uses the existing WeChat media queue and a
// synthetic native receipt. No real WeChat account, server, or phone is used.
func TestDocumentWorkshopPDFOnlyDeliveryUsesCoreQueueAndReceipt(t *testing.T) {
	f, p, _, _ := artifactPipelineFixture(t)
	native := &documentDeliveryNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native

	f.manager.mu.Lock()
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "documents@chatroom", Mode: "mention"})
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.mu.Unlock()
	settings := f.manager.agent.config()
	settings.CrossConversation = true
	settings.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}

	text := "把图片复刻为 Word，再只把 PDF 发到这个群"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "documents@chatroom", Sender: "wxid_owner", TraceID: "document-pdf-request", OriginTraceID: "document-pdf-request", IsGroup: true, Text: text, OriginalText: &text, MessageTime: now, Source: &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: "document-pdf-request", ServerID: "synthetic-document-pdf-request", Account: "test", Thread: "documents@chatroom", Sender: "wxid_owner", MessageType: 1, OccurredAt: now, Revision: 1, Integrity: "verified"}}
	if err := f.manager.memory.Observe(input, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	if source, ok := f.manager.memory.SourceForInput(input); ok {
		input.Source = source
	} else {
		t.Fatal("missing durable source for document delivery request")
	}
	p.SetTrace(input)

	docxBytes := []byte("synthetic editable docx source")
	pdfBytes := []byte("%PDF-1.7\nsynthetic verified document\n%%EOF\n")
	docx, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: docxBytes, MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Filename: "rebuilt.docx"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: pdfBytes, MIME: "application/pdf", Filename: "rebuilt.pdf"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]string{"source_thread_id": "documents@chatroom", "target_thread_id": "documents@chatroom", "asset_id": pdf.ID, "kind": "file", "request_key": "document-pdf-only"})
	result, err := p.Call(context.Background(), "wechat_send_media", args)
	if err != nil || !strings.Contains(result, `"state":"queued"`) || !strings.Contains(result, `"target_thread_id":"documents@chatroom"`) {
		t.Fatalf("PDF delivery was not queued through core transport: result=%s err=%v", result, err)
	}
	seeds := f.manager.takeMediaSeeds()
	if len(seeds) != 1 || seeds[0].Media.AssetID != pdf.ID || seeds[0].Media.Kind != "file" || native.submits.Load() != 0 {
		t.Fatalf("queue selected anything other than the PDF: seeds=%#v submits=%d", seeds, native.submits.Load())
	}
	if _, err = native.Submit(context.Background(), linuxnative.ToolSend{ThreadID: seeds[0].Input.Thread, Kind: seeds[0].Media.Kind, SourceID: seeds[0].SourceID, IdempotencyKey: seeds[0].Key}); err != nil {
		t.Fatal(err)
	}
	var queued struct {
		ID string `json:"operation_id"`
	}
	if err = json.Unmarshal([]byte(result), &queued); err != nil || queued.ID == "" {
		t.Fatalf("missing queued PDF operation: %s (%v)", result, err)
	}
	statusArgs, _ := json.Marshal(map[string]string{"operation_id": queued.ID})
	status, err := p.Call(context.Background(), "wechat_operation_status", statusArgs)
	if err != nil || !strings.Contains(status, `"state":"accepted"`) || len(f.manager.takeMediaSeeds()) != 0 {
		t.Fatalf("PDF acceptance was not recorded: status=%s err=%v seeds=%d", status, err, len(f.manager.takeMediaSeeds()))
	}
	native.mu.Lock()
	uploads := append([][]byte(nil), native.uploads...)
	sends := append([]linuxnative.ToolSend(nil), native.sends...)
	native.mu.Unlock()
	if len(uploads) != 1 || string(uploads[0]) != string(pdfBytes) || len(sends) != 1 || sends[0].ThreadID != "documents@chatroom" || sends[0].Kind != "file" {
		t.Fatalf("only real PDF bytes should reach the selected group: uploads=%q sends=%#v", uploads, sends)
	}

	// The editable DOCX was never queued or uploaded; it remains available for a
	// later title/layout edit in the same authorized task scope.
	materialized, err := p.MaterializeArtifactForTask(context.Background(), docx.ID, "document-pdf-request", "")
	if err != nil {
		t.Fatal(err)
	}
	reusable, err := os.ReadFile(materialized.InputPath)
	if err != nil || string(reusable) != string(docxBytes) || string(uploads[0]) == string(docxBytes) {
		t.Fatalf("editable DOCX was lost or accidentally sent: reusable=%q err=%v", reusable, err)
	}
}
