//go:build windows

package pluginruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

type templateEchoPolicy struct{}

func (templateEchoPolicy) Check(context.Context, pluginhost.Job, pluginhost.Capability) error { return nil }
func (templateEchoPolicy) Validate(_ context.Context, _ pluginhost.Job, _ pluginhost.Capability, raw json.RawMessage) (VerifiedResult, error) {
	return VerifiedResult{Result: append(json.RawMessage(nil), raw...)}, nil
}

// TestTemplatePythonPackageWindowsLPACContract is deliberately opt-in. It
// imports the public template into a temporary account store and crosses the
// real Windows broker/controller/stdio boundary without touching an installed
// account, a provider, or WeChat.
func TestTemplatePythonPackageWindowsLPACContract(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("set WECHAT_PLUGIN_TEST_PYTHON for the Windows LPAC template contract")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	store, err := pluginhost.OpenStore(t.TempDir(), "template-isolated-account")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepared, err := store.Prepare(t.Context(), "../../templates/plugins/python-stdio")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	installed, err = store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(store, InstalledExecutor(store, Options{
		Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"),
		WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err = controller.Start(ctx, installed.Package.ID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	request := CallRequest{Plugin: installed.Package.ID, Capability: "echo", Context: pluginhost.Invocation{
		Account: "template-isolated-account", Thread: "template-thread", Requester: "template-user",
		Purpose: "Windows LPAC template contract", RequestKey: "echo-once",
	}, Arguments: json.RawMessage(`{"text":"模板隔离回显"}`)}
	first, err := controller.Invoke(ctx, request, templateEchoPolicy{})
	if err != nil || first.Job.State != "completed" || !json.Valid(first.Job.Result) {
		t.Fatalf("template call failed: job=%+v err=%v", first.Job, err)
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	var result struct{ Text string `json:"text"` }
	if json.Unmarshal(first.Job.Result, &envelope) != nil || envelope.IsError || len(envelope.Content) != 1 || envelope.Content[0].Type != "text" || json.Unmarshal([]byte(envelope.Content[0].Text), &result) != nil || result.Text != "模板隔离回显" {
		t.Fatalf("wrong isolated template result: %s", first.Job.Result)
	}
	second, err := controller.Invoke(ctx, request, templateEchoPolicy{})
	if err != nil || !second.Reused || second.Job.ID != first.Job.ID {
		t.Fatalf("same request key was not safely reused: first=%+v second=%+v err=%v", first.Job, second.Job, err)
	}
}
