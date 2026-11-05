package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginruntime"
)

// localProjectToolWindowsOptions deliberately receives its runtime from an
// explicit deployment directory.  It never searches PATH: this test is proof
// of the packaged/isolated Windows path, not a host-Python fallback.
func localProjectToolWindowsOptions(t *testing.T) pluginruntime.Options {
	t.Helper()
	runtimeRoot := strings.TrimSpace(os.Getenv("AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT"))
	if runtimeRoot == "" {
		t.Fatal("set AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT to the Windows preview runtimes directory")
	}
	runtimeRoot, err := filepath.Abs(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := filepath.Abs("../../plugins")
	if err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(runtimeRoot, "windows-python-cli")
	options := pluginruntime.Options{
		Python:           filepath.Join(python, "python.exe"),
		Pythonw:          filepath.Join(python, "pythonw.exe"),
		WindowsHelper:    filepath.Join(runtimeRoot, "windows-agent-host", "windows_plugin_host.pyw"),
		RuntimeRoots:     []string{python},
		CatalogDirectory: catalog,
	}
	for _, path := range []string{
		options.Python, options.Pythonw, options.WindowsHelper,
		filepath.Join(filepath.Dir(options.WindowsHelper), "windows_lpac.py"),
		filepath.Join(options.CatalogDirectory, documentWorkshopPluginID, "plugin.json"),
	} {
		if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("Windows project-tool runtime unavailable: %s (%v)", path, statErr)
		}
	}
	return options
}

func localProjectToolsView(t *testing.T, f *localHomeFixture) map[string]any {
	t.Helper()
	code, page := f.request("GET", "/api/local/project-tools", nil, true)
	if code != http.StatusOK {
		t.Fatalf("project tools list: %d %#v", code, page)
	}
	tools, ok := page["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("unexpected project tool list: %#v", page)
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["id"] != documentWorkshopPluginID {
		t.Fatalf("document workshop missing: %#v", page)
	}
	return tool
}

func localProjectToolRevision(t *testing.T, tool map[string]any) int {
	t.Helper()
	value, ok := tool["revision"].(float64)
	if !ok || value < 1 {
		t.Fatalf("invalid project tool revision: %#v", tool)
	}
	return int(value)
}

func localArtifactDownload(t *testing.T, f *localHomeFixture, id string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.server.URL+"/api/local/artifacts/"+id+"/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", f.server.URL)
	req.Header.Set("X-CSRF-Token", f.csrf)
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Disposition"), "attachment") || len(body) == 0 {
		t.Fatalf("artifact download: %s %#v", response.Status, response.Header)
	}
	return body
}

func localDOCXDocumentXML(t *testing.T, body []byte) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("artifact is not a DOCX archive: %v", err)
	}
	for _, file := range reader.File {
		if file.Name != "word/document.xml" {
			continue
		}
		stream, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		data, readErr := io.ReadAll(stream)
		stream.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(data)
	}
	t.Fatal("DOCX has no word/document.xml")
	return ""
}

// TestLiveLocalProjectToolCreatesImmutableDocumentThroughChat proves the
// first zero-application project-tool story end-to-end.  It has no provider
// model and no WeChat transport: the model is a deterministic local fixture,
// while document-workshop itself runs through the installed MCP + Windows
// LPAC path and produces a real editable DOCX registered in the local
// immutable artifact store.
func TestLiveLocalProjectToolCreatesImmutableDocumentThroughChat(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("AHS_LOCAL_PROJECT_DOCUMENT_LPAC_TEST") != "1" {
		t.Skip("set AHS_LOCAL_PROJECT_DOCUMENT_LPAC_TEST=1 on Windows for local project-tool LPAC acceptance")
	}
	options := localProjectToolWindowsOptions(t)
	base := t.TempDir()
	f := newLocalHomeFixture(t, filepath.Join(base, "host"), filepath.Join(base, "workspace"))
	f.handler.chat.projectTools.options = options
	if code, _ := f.request("GET", "/api/local/project-tools", nil, false); code != http.StatusUnauthorized {
		t.Fatalf("project tools exposed before local-owner login: %d", code)
	}
	f.login()
	if code, out := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatalf("initialize local home: %d %#v", code, out)
	}

	initial := localProjectToolsView(t, f)
	if initial["catalog_available"] != true || initial["runtime_ready"] != true || initial["installed"] != false || initial["state"] != "not_installed" {
		t.Fatalf("unexpected zero-app tool state: %#v", initial)
	}
	if code, _ := f.request("PUT", "/api/local/project-tools/document-workshop", map[string]any{"action": "install"}, true); code != http.StatusBadRequest {
		t.Fatalf("document tool installed without artifact permission: %d", code)
	}
	if code, out := f.request("PUT", "/api/local/project-tools/document-workshop", map[string]any{"action": "install", "approve_artifacts": true}, true); code != http.StatusOK {
		t.Fatalf("install document tool: %d %#v", code, out)
	}
	installed := localProjectToolsView(t, f)
	if installed["installed"] != true || installed["enabled"] != false || installed["state"] != "disabled" {
		t.Fatalf("document tool was not installed but disabled: %#v", installed)
	}
	if code, out := f.request("PUT", "/api/local/project-tools/document-workshop", map[string]any{"action": "enable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatalf("enable document tool: %d %#v", code, out)
	}
	enabled := localProjectToolsView(t, f)
	if enabled["enabled"] != true || enabled["state"] != "idle" {
		t.Fatalf("document tool not idle before chat invocation: %#v", enabled)
	}

	const paragraph = "大肥鱼的本机工具成品验收。"
	const cell = "可编辑表格单元格"
	const filename = "local-project-acceptance.docx"
	args := map[string]any{
		"layout": map[string]any{"blocks": []any{
			map[string]any{"kind": "paragraph", "text": paragraph},
			map[string]any{"kind": "table", "rows": []any{[]any{"项目", "结论"}, []any{"本机成品", cell}}},
		}},
		"filename": filename,
		"title":    "本机项目工具验收",
	}
	captureArtifact := func(turns []map[string]string) string {
		all := localModelHistory(turns)
		start := strings.LastIndex(all, "work-")
		if start >= 0 && len(all) >= start+69 {
			if candidate := all[start : start+69]; artifactID(candidate, "work-") {
				return candidate
			}
		}
		t.Error("project tool result did not return a registered artifact id", all)
		return ""
	}
	var artifact string
	calls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, turns []map[string]string) (string, error) {
			calls++
			switch calls {
			case 1:
				if !strings.Contains(system, "project_document_create") {
					t.Error("local chat did not disclose the installed project document tool")
				}
				return localToolCall("project_document_create", args), nil
			default:
				artifact = captureArtifact(turns)
				return `{"action":"reply","messages":["文档已生成并登记：` + artifact + `。"]}`, nil
			}
		}), nil
	}
	if code, out := localChatSend(f, "local-document-project-tool", "做一份可编辑的中文文档并保存成品"); code != http.StatusOK || out["status"] != "completed" {
		t.Fatalf("normal local chat document flow: %d %#v", code, out)
	}
	if calls != 2 || !artifactID(artifact, "work-") {
		t.Fatalf("unexpected local document calls=%d artifact=%q", calls, artifact)
	}
	if code, view := f.request("GET", "/api/local/artifacts/"+artifact, nil, true); code != http.StatusOK || view["filename"] != filename || view["status"] != "registered" {
		t.Fatalf("registered local document artifact missing: %d %#v", code, view)
	}
	xml := localDOCXDocumentXML(t, localArtifactDownload(t, f, artifact))
	if !strings.Contains(xml, paragraph) || !strings.Contains(xml, cell) || !strings.Contains(xml, "<w:tbl") {
		t.Fatalf("downloaded DOCX is not an editable Chinese document: %s", xml)
	}

	// Verify the already started real session can be recycled without
	// disabling the installed tool or affecting its immutable artifact.
	host := f.handler.chat.projectTools.host
	if state := host.controller.Status(documentWorkshopPluginID); state.State != "running" || !state.AutoManaged {
		t.Fatal("document tool did not hold an on-demand runtime lease", state)
	}
	host.controller.SetIdleTimeout(30 * time.Millisecond)
	until := time.Now().Add(5 * time.Second)
	for host.controller.Status(documentWorkshopPluginID).State != "stopped" && time.Now().Before(until) {
		time.Sleep(20 * time.Millisecond)
	}
	if view := localProjectToolsView(t, f); view["state"] != "idle" || view["enabled"] != true {
		t.Fatalf("idle recycle changed the configured grant or did not stop: %#v", view)
	}

	// A restart must retain the immutable artifact and must replay the original
	// request from durable chat state rather than invoking another generator.
	next := localRestartChat(t, f)
	next.handler.chat.projectTools.options = options
	next.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		t.Error("restart replay called a document generator")
		return nil, context.Canceled
	}
	if code, out := localChatSend(next, "local-document-project-tool", "做一份可编辑的中文文档并保存成品"); code != http.StatusOK || out["reused"] != true {
		t.Fatalf("restart replay regenerated document: %d %#v", code, out)
	}
	if xml = localDOCXDocumentXML(t, localArtifactDownload(t, next, artifact)); !strings.Contains(xml, paragraph) || !strings.Contains(xml, "<w:tbl") {
		t.Fatal("restart lost immutable editable DOCX")
	}

	// Interruption after the project tool, before the final chat reply, also
	// resumes through the original tool checkpoint without starting a process.
	const retryKey, retryText = "local-document-interrupted", "再做一份，保留本机成品"
	interruptedArtifact := ""
	next.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		round := 0
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			round++
			if round == 1 {
				return localToolCall("project_document_create", args), nil
			}
			interruptedArtifact = captureArtifact(turns)
			return "", context.Canceled
		}), nil
	}
	if code, out := localChatSend(next, retryKey, retryText); code != http.StatusBadGateway || !artifactID(interruptedArtifact, "work-") || interruptedArtifact == artifact {
		t.Fatalf("interrupted project tool fixture: %d %#v id=%q", code, out, interruptedArtifact)
	}
	recovered := localRestartChat(t, next)
	recovered.handler.chat.projectTools.options = options
	recovered.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		round := 0
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			round++
			if round == 1 {
				return localToolCall("project_document_create", args), nil
			}
			id := captureArtifact(turns)
			if id != interruptedArtifact || !strings.Contains(localModelHistory(turns), "recovered") {
				t.Error("resumed document operation did not return the original artifact", id)
			}
			return `{"action":"reply","messages":["文档已登记：` + id + `。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(recovered, retryKey, retryText); code != http.StatusOK || out["status"] != "completed" {
		t.Fatalf("could not resume interrupted document reply: %d %#v", code, out)
	}
	if view := localProjectToolsView(t, recovered); view["state"] != "idle" {
		t.Fatalf("resuming interrupted request started the document process again: %#v", view)
	}
}
