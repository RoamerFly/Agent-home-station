package station

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/plugins"
)

func TestWechatExternalCatalogUsesCurrentWorkstationRegistration(t *testing.T) {
	if os.Getenv("AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT") == "" {
		t.Skip("Windows project-tool runtime is required for catalog integration")
	}
	f := localChatFixture(t)
	f.handler.chat.projectTools.options = localProjectToolWindowsOptionsFor(t, localExternalPackage)
	if code, out := f.request("GET", "/api/local/project-tools", nil, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	route := "/api/local/project-tools/" + localExternalPackage
	if code, out := f.request("PUT", route, map[string]any{"action": "install", "approve_artifacts": true}, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	installed := localProjectToolByID(t, f, localExternalPackage)
	if code, out := f.request("PUT", route, map[string]any{"action": "enable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	const secret = "synthetic-private-key"
	registration := map[string]any{"connections": []plugins.Installed{{Manifest: plugins.Manifest{ID: "synthetic", Name: "Synthetic", Version: "1.0.0",
		Tools: []plugins.APITool{{Name: "lookup", Description: "Read a synthetic record", URL: "https://fixture.example.test/read", Method: "GET",
			Headers:     map[string]string{"Authorization": "Bearer {{secret.api_key}}"},
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}},
		Enabled: true, Secrets: map[string]string{"api_key": secret}}}}
	path := stationToolRegistrationPath(f.handler.home.dataDir, localExternalRegistration)
	raw, _ := json.Marshal(registration)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "native-token")
	if err := os.WriteFile(key, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:1", TokenFile: key, Account: "synthetic-account"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	in := companion.Input{Channel: "wechat", Account: native.Account(), Thread: "test@chatroom", Sender: "owner", TraceID: "synthetic-catalog",
		MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true}
	adapter := &stationAttachmentTools{native: native, projects: f.handler.chat.projectTools,
		grants: map[string]time.Time{in.Thread: now.Add(-time.Minute)}, blocked: func() bool { return false },
		projectOwnerAllowed: func(current companion.Input) bool { return current.Sender == in.Sender }}
	adapter.SetTrace(in)
	listed, err := adapter.List(t.Context())
	if err != nil || !hasTool(listed, localExternalHTTPList) || hasTool(listed, localExternalHTTPCall) {
		t.Fatal("application catalog did not distinguish discovery from execution", err)
	}
	result, err := adapter.Call(t.Context(), localExternalHTTPList, json.RawMessage(`{"limit":1}`))
	if err != nil || !strings.Contains(result, "synthetic__lookup") || strings.Contains(result, secret) || strings.Contains(result, "fixture.example.test") {
		t.Fatal("workstation catalog leaked private connection settings", result, err)
	}
	localStore, localInput, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	public := f.handler.chat.tools(localStore, localInput)
	public.publicUseOnly = true
	public.run = companion.NewGuardedToolRun(func(context.Context) error { return nil })
	listed, err = public.List(t.Context())
	if err != nil || hasTool(listed, localExternalHTTPList) || hasTool(listed, localExternalHTTPCall) {
		t.Fatal("source-bound HTTP tools leaked into the application", err)
	}
	if _, err := public.Call(t.Context(), localExternalHTTPCall, json.RawMessage(`{}`)); err == nil {
		t.Fatal("application bypassed its external tool adapter")
	}
	installed = localProjectToolByID(t, f, localExternalPackage)
	if code, out := f.request("PUT", route, map[string]any{"action": "disable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	listed, err = adapter.List(t.Context())
	if err != nil || hasTool(listed, localExternalHTTPList) {
		t.Fatal("disabled workstation catalog remained visible", err)
	}
	if _, err := adapter.Call(t.Context(), localExternalHTTPList, json.RawMessage(`{}`)); err == nil {
		t.Fatal("disabled workstation catalog remained callable")
	}
}

func TestWechatExternalHTTPTextSubmitOnce(t *testing.T) {
	if os.Getenv("AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT") == "" {
		t.Skip("Windows project-tool runtime is required")
	}
	f := localChatFixture(t)
	f.handler.chat.projectTools.options = localProjectToolWindowsOptionsFor(t, localExternalPackage)
	if code, out := f.request("GET", "/api/local/project-tools", nil, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	route := "/api/local/project-tools/" + localExternalPackage
	for _, action := range []string{"install", "enable"} {
		input := map[string]any{"action": action}
		if action == "install" {
			input["approve_artifacts"] = true
		} else {
			input["revision"] = localProjectToolRevision(t, localProjectToolByID(t, f, localExternalPackage))
		}
		if code, out := f.request("PUT", route, input, true); code != http.StatusOK {
			t.Fatal(code, out)
		}
	}
	var requests atomic.Int32
	var fileRequests, uploads, mediaRequests, mediaFetches atomic.Int32
	const secret = "synthetic-private-key"
	binary := []byte("synthetic binary external response")
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts/synthetic-account/uploads" {
			file, header, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			body, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(body, binary) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			uploads.Add(1)
			fmt.Fprintf(w, `{"id":"synthetic-external-upload","name":%q,"byteLength":%d,"sha256":%q}`, header.Filename, len(body), externalDigest(body))
			return
		}
		if r.URL.Path == "/asset" {
			if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "ticket=private" {
				t.Error("media request leaked credential or lost signed reference")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mediaFetches.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binary)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("host credential missing")
		}
		if r.URL.Path == "/file" {
			fileRequests.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binary)
			return
		}
		if r.URL.Path == "/media" {
			mediaRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"asset_url":"` + server.URL + `/asset?ticket=private"}`))
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"first second synthetic-private-key"}`))
	}))
	defer server.Close()
	registration := map[string]any{"connections": []plugins.Installed{{Manifest: plugins.Manifest{ID: "synthetic", Name: "Synthetic", Version: "1.0.0",
		Tools: []plugins.APITool{{Name: "lookup", URL: server.URL + "/text", Method: "GET", TextPath: "result",
			Headers:     map[string]string{"Authorization": "Bearer {{secret.api_key}}"},
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
			{Name: "file", URL: server.URL + "/file", Method: "GET", MediaKind: "file",
				Headers:     map[string]string{"Authorization": "Bearer {{secret.api_key}}"},
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
			{Name: "media", URL: server.URL + "/media", Method: "GET", MediaKind: "file", MediaURLPath: "asset_url",
				Headers:     map[string]string{"Authorization": "Bearer {{secret.api_key}}"},
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}},
		Enabled: true, Secrets: map[string]string{"api_key": secret}}}}
	path := stationToolRegistrationPath(f.handler.home.dataDir, localExternalRegistration)
	raw, _ := json.Marshal(registration)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "native-token")
	if err := os.WriteFile(key, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: server.URL, TokenFile: key, Account: "synthetic-account"})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	now := time.Now().UTC()
	in := companion.Input{Channel: "wechat", Account: native.Account(), Thread: "test@chatroom", Sender: "owner", TraceID: "synthetic-http", Text: "查找",
		MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true}
	in.Source = &companion.SourceRef{InstanceID: memory.SourceInstanceID(), MessageID: in.TraceID, Account: in.Account,
		Thread: in.Thread, Sender: in.Sender, OccurredAt: in.MessageTime}
	config := companion.Defaults()
	config.Permissions.Remember = true
	if err := memory.Observe(in, config, now); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = memory.SourceForInput(in)
	grant := now.Add(-time.Minute)
	adapter := &stationAttachmentTools{native: native, store: memory, dir: t.TempDir(), projects: f.handler.chat.projectTools,
		grants: map[string]time.Time{in.Thread: grant}, replies: map[string]time.Time{in.Thread: grant}, blocked: func() bool { return false }, externalMu: new(sync.Mutex),
		projectOwnerAllowed: func(current companion.Input) bool { return current.Sender == in.Sender }}
	adapter.SetTrace(in)
	listed, err := adapter.List(t.Context())
	if err != nil || !hasTool(listed, localExternalHTTPCall) {
		t.Fatal("external HTTP call not available", err)
	}
	call := json.RawMessage(`{"tool":"synthetic__lookup","arguments":{},"text_limit":6}`)
	first, err := adapter.Call(t.Context(), localExternalHTTPCall, call)
	if err != nil || requests.Load() != 1 || !strings.Contains(first, "first ") || strings.Contains(first, secret) {
		t.Fatal("first call failed", first, err, requests.Load())
	}
	second, err := adapter.Call(t.Context(), localExternalHTTPCall, json.RawMessage(`{"tool":"synthetic__lookup","arguments":{},"text_offset":6}`))
	if err != nil || requests.Load() != 1 || !strings.Contains(second, "reused\":true") || strings.Contains(second, secret) {
		t.Fatal("continuation repeated request or leaked secret", second, err, requests.Load())
	}
	fileResult, err := adapter.Call(t.Context(), localExternalHTTPCall, json.RawMessage(`{"tool":"synthetic__file","arguments":{}}`))
	var mediaView struct {
		State string                     `json:"state"`
		Media []companion.GeneratedMedia `json:"media"`
	}
	if err != nil || json.Unmarshal([]byte(fileResult), &mediaView) != nil || mediaView.State != "completed" || len(mediaView.Media) != 1 || fileRequests.Load() != 1 {
		t.Fatal("binary external result was not prepared", fileResult, err, fileRequests.Load())
	}
	if _, ok := adapter.Media(mediaView.Media[0].ID); !ok {
		t.Fatal("binary external result was not bound to original message")
	}
	if uploaded, err := adapter.stageReply(t.Context(), in, "file", mediaView.Media[0].AssetID); err != nil || uploaded != "synthetic-external-upload" || uploads.Load() != 1 {
		t.Fatal("binary external result could not be staged", uploaded, err, uploads.Load())
	}
	mediaCall := json.RawMessage(`{"tool":"synthetic__media","arguments":{}}`)
	pending, err := adapter.Call(t.Context(), localExternalHTTPCall, mediaCall)
	if err != nil || !strings.Contains(pending, `"state":"media_pending"`) || strings.Contains(pending, server.URL) ||
		mediaRequests.Load() != 1 || mediaFetches.Load() != 0 {
		t.Fatal("sync media URL was not held privately", pending, err)
	}
	collected, err := adapter.Call(t.Context(), localExternalHTTPCall, mediaCall)
	var collectedView struct {
		State string                     `json:"state"`
		Media []companion.GeneratedMedia `json:"media"`
	}
	if err != nil || json.Unmarshal([]byte(collected), &collectedView) != nil || collectedView.State != "completed" ||
		len(collectedView.Media) != 1 || mediaRequests.Load() != 1 || mediaFetches.Load() != 1 || strings.Contains(collected, server.URL) {
		t.Fatal("sync media was not collected without resubmission", collected, err)
	}
	if uploaded, err := adapter.stageReply(t.Context(), in, "file", collectedView.Media[0].AssetID); err != nil ||
		uploaded != "synthetic-external-upload" || uploads.Load() != 2 {
		t.Fatal("collected sync media could not be staged", uploaded, err)
	}
	delete(adapter.replies, in.Thread)
	if _, err := adapter.stageReply(t.Context(), in, "file", mediaView.Media[0].AssetID); err == nil || uploads.Load() != 2 {
		t.Fatal("revoked media grant still allowed upload")
	}
	adapter.replies[in.Thread] = grant
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := adapter.Call(t.Context(), localExternalHTTPCall, call)
	if err != nil || !strings.Contains(changed, "needs_review") || requests.Load() != 1 {
		t.Fatal("registration change resubmitted original request", changed, err, requests.Load())
	}
	installed := localProjectToolByID(t, f, localExternalPackage)
	if code, out := f.request("PUT", route, map[string]any{"action": "disable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	if _, err := adapter.Call(t.Context(), localExternalHTTPCall, call); err == nil || requests.Load() != 1 {
		t.Fatal("disabled external HTTP call remained available")
	}
}
