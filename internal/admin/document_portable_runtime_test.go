package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func TestDocumentPortableTaskRuntime(t *testing.T) {
	root := os.Getenv("STATION_DOCUMENT_TEST_RELEASE")
	if root == "" {
		t.Skip("set STATION_DOCUMENT_TEST_RELEASE for installed Windows runtime")
	}
	options, _ := json.Marshal(map[string]any{"python": filepath.Join(root, "env/windows-python-cli/python.exe"), "pythonw": filepath.Join(root, "env/windows-python-cli/pythonw.exe"), "windows_helper": filepath.Join(root, "env/windows-agent-host/windows_plugin_host.pyw"), "runtime_roots": []string{filepath.Join(root, "env/windows-python-cli"), filepath.Join(root, "env/windows-agent-host")}, "document_runtime_root": filepath.Join(root, "env/document-workshop")})
	t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", string(options))
	_, p, _, _ := artifactPipelineFixture(t)
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for name, text := range map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>synthetic 7319</w:t></w:r></w:p></w:body></w:document>`} {
		w, _ := z.Create(name)
		_, _ = w.Write([]byte(text))
	}
	_ = z.Close()
	asset, err := p.manager.rememberAssetFormats(p.trace, assetOriginal{Data: data.Bytes(), MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Filename: "sample.docx"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	installed := installDocumentWorkshopFixture(t, p)
	policy, arguments, err := p.newDocumentWorkshopPolicy(context.Background(), "document.inspect", json.RawMessage(`{"artifact_id":"`+asset.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := p.documentWorkshopTaskRuntime(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err = runtime.Start(context.Background(), documentWorkshopPluginID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Invoke(context.Background(), pluginruntime.CallRequest{Plugin: documentWorkshopPluginID, Capability: "document.inspect", Context: policy.invocation, Arguments: arguments}, documentRuntimeTestPolicy{policy, t})
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.State != "completed" {
		t.Fatal(result.Job.State)
	}
}

type documentRuntimeTestPolicy struct {
	*documentWorkshopPolicy
	t *testing.T
}

func (p documentRuntimeTestPolicy) Validate(ctx context.Context, j pluginhost.Job, c pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	out, err := p.documentWorkshopPolicy.Validate(ctx, j, c, raw)
	if err != nil {
		p.t.Log("synthetic document validation", string(raw), err)
	}
	return out, err
}
