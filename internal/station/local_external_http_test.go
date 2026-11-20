package station

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

func TestLiveStationExternalHTTPThroughChat(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("AHS_LOCAL_EXTERNAL_HTTP_TEST") != "1" {
		t.Skip("set AHS_LOCAL_EXTERNAL_HTTP_TEST=1 for installed Windows runtime acceptance")
	}
	options := localProjectToolWindowsOptionsFor(t, localExternalPackage)
	base := t.TempDir()
	f := newLocalHomeFixture(t, filepath.Join(base, "host"), filepath.Join(base, "workspace"))
	f.handler.chat.projectTools.options = options
	f.login()
	if code, _ := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatal("isolated station could not initialize")
	}
	const route = "/api/local/project-tools/" + localExternalPackage
	if code, _ := f.request("PUT", route, map[string]any{"action": "install", "approve_artifacts": true}, true); code != http.StatusOK {
		t.Fatalf("external package installation: %d", code)
	}
	if code, _ := f.request("PUT", route, map[string]any{"action": "enable", "revision": localProjectToolRevision(t, localProjectToolByID(t, f, localExternalPackage))}, true); code != http.StatusOK {
		t.Fatalf("external package enable: %d", code)
	}
	if state := localProjectToolByID(t, f, localExternalPackage); !strings.Contains(state["reason"].(string), "配置同步 HTTP") {
		t.Fatal("unconfigured package status does not explain missing registration")
	}

	const secret = "synthetic-external-credential"
	binary := []byte("synthetic external immutable file\r\n")
	var textCalls, fileCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("approved credential not injected by the host")
			http.Error(w, "missing bound credential", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/structured":
			var args struct{ Item string }
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&args) != nil || args.Item != "station-fixture" {
				t.Error("declared HTTP request did not reach approved endpoint")
				http.Error(w, "wrong request", http.StatusBadRequest)
				return
			}
			textCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"result": "verified-http-result " + secret})
		case "/file":
			if r.Method != http.MethodGet {
				http.Error(w, "wrong method", http.StatusBadRequest)
				return
			}
			fileCalls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binary)
		default:
			t.Error("unapproved path contacted")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	emptySchema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	manifest := plugins.Manifest{ID: "acceptance", Name: "External HTTP fixtures", Version: "1.0.0",
		Tools: []plugins.APITool{
			{Name: "text", Description: "Fetch structured fixture", URL: server.URL + "/structured", Method: "POST", TextPath: "result",
				Headers:     map[string]string{"Authorization": "Bearer {{secret.api_key}}"},
				InputSchema: json.RawMessage(`{"type":"object","required":["item"],"properties":{"item":{"type":"string"}},"additionalProperties":false}`)},
			{Name: "file", Description: "Fetch binary fixture", URL: server.URL + "/file", Method: "GET", MediaKind: "file",
				Headers: map[string]string{"Authorization": "Bearer {{secret.api_key}}"}, InputSchema: emptySchema},
		},
	}
	public := os.Getenv("AHS_LOCAL_EXTERNAL_HTTP_PUBLIC") == "1"
	if public {
		manifest.Tools = append(manifest.Tools, plugins.APITool{Name: "public", Description: "Get public repository metadata",
			URL: "https://api.github.com/repos/octocat/Hello-World", Method: "GET", TextPath: "full_name", InputSchema: emptySchema})
	}
	config := map[string]any{"connections": []plugins.Installed{{Manifest: manifest, Enabled: true, Secrets: map[string]string{"api_key": secret}}}}
	registration := stationToolRegistrationPath(f.handler.home.dataDir, localExternalRegistration)
	raw, _ := json.Marshal(config)
	if os.MkdirAll(filepath.Dir(registration), 0700) != nil || os.WriteFile(registration, raw, 0600) != nil {
		t.Fatal("could not prepare isolated owner registration")
	}

	round := 0
	var outputID string
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, turns []map[string]string) (string, error) {
			round++
			history := localModelHistory(turns)
			if strings.Contains(history+system, secret) || strings.Contains(history+system, server.URL) {
				t.Error("host-only registration or credential reached the model")
			}
			switch round {
			case 1:
				if !strings.Contains(system, localExternalHTTPCall) {
					t.Error("registered external HTTP tool not discoverable in main chat")
					return `{"action":"reply","messages":["工具不可用。"]}`, nil
				}
				return localToolCall(localExternalHTTPList, map[string]any{"query": "acceptance__", "limit": 1}), nil
			case 2:
				if !strings.Contains(history, "acceptance__file") || !strings.Contains(history, "has_more") {
					t.Error("paginated directory did not return the approved declaration")
				}
				return localToolCall(localExternalHTTPCall, map[string]any{"tool": "acceptance__text", "arguments": map[string]string{"item": "station-fixture"}}), nil
			case 3:
				if !strings.Contains(history, "verified-http-result") || !strings.Contains(history, "[已隐藏]") {
					t.Error("verified text or credential masking did not reach chat")
				}
				return localToolCall(localExternalHTTPCall, map[string]any{"tool": "acceptance__text", "arguments": map[string]string{"item": "station-fixture"}, "text_offset": 9, "text_limit": 4}), nil
			case 4:
				if last := turns[len(turns)-1]["content"]; !strings.Contains(last, `\"reused\":true`) || !strings.Contains(last, `\"next_text_offset\":13`) || !strings.Contains(last, `\"text\":\"http\"`) {
					t.Error("paging did not reuse the original network observation")
				}
				return localToolCall(localExternalHTTPCall, map[string]any{"tool": "acceptance__file", "arguments": map[string]any{}}), nil
			case 5:
				ids := projectArtifactIDs(turns)
				if len(ids) != 1 {
					t.Error("binary result was not registered")
				} else {
					outputID = ids[0]
				}
				if public {
					return localToolCall(localExternalHTTPCall, map[string]any{"tool": "acceptance__public", "arguments": map[string]any{}}), nil
				}
			case 6:
				if !strings.Contains(history, "octocat/Hello-World") {
					t.Error("actual public repository response did not reach chat")
				}
			}
			return `{"action":"reply","messages":["已取得核验结果。"]}`, nil
		}), nil
	}
	code, out := localChatSend(f, "external-tools-http", "查看已授权外部工具并读取测试结果。")
	if code != http.StatusOK || out["status"] != "completed" || textCalls.Load() != 1 || fileCalls.Load() != 1 || round < 5 || outputID == "" {
		t.Fatalf("external HTTP chat failed: status=%d done=%v text=%d file=%d rounds=%d artifact=%t", code, out["status"], textCalls.Load(), fileCalls.Load(), round, outputID != "")
	}
	if download := localArtifactDownload(t, f, outputID); !bytes.Equal(binary, download) {
		t.Fatal("external artifact bytes could not be verified")
	}

	const retryKey, retryMessage = "external-http-recovery", "读取外部测试数据，然后告诉我结果。"
	retryArgs := map[string]any{"tool": "acceptance__text", "arguments": map[string]string{"item": "station-fixture"}}
	round = 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			round++
			if round == 1 {
				return localToolCall(localExternalHTTPCall, retryArgs), nil
			}
			if !strings.Contains(localModelHistory(turns), "verified-http-result") {
				t.Error("HTTP observation missing before interrupted reply")
			}
			return "", context.Canceled
		}), nil
	}
	if code, _ := localChatSend(f, retryKey, retryMessage); code != http.StatusBadGateway || textCalls.Load() != 2 {
		t.Fatalf("expected interrupted reply after exactly one HTTP call: status=%d total_calls=%d", code, textCalls.Load())
	}
	next := localRestartChat(t, f)
	next.handler.chat.projectTools.options = options
	round = 0
	next.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			round++
			if round == 1 {
				return localToolCall(localExternalHTTPCall, retryArgs), nil
			}
			last := turns[len(turns)-1]["content"]
			if !strings.Contains(last, "verified-http-result") || !strings.Contains(last, `\"reused\":true`) {
				t.Error("reopened host did not recover the verified result")
			}
			return `{"action":"reply","messages":["已读取原请求的结果。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(next, retryKey, retryMessage); code != http.StatusOK || out["status"] != "completed" || textCalls.Load() != 2 {
		t.Fatalf("external HTTP retry did not reuse the original result: status=%d total_calls=%d", code, textCalls.Load())
	}

	if err := os.Remove(registration); err != nil {
		t.Fatal(err)
	}
	next.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, _ []map[string]string) (string, error) {
			if strings.Contains(system, localExternalHTTPCall) {
				t.Error("revoked registration left the tool available")
			}
			return `{"action":"reply","messages":["已检查登记。"]}`, nil
		}), nil
	}
	if code, _ := localChatSend(next, "external-tools-revoked", "检查已撤销的连接。"); code != http.StatusOK {
		t.Fatalf("post-revocation chat failed: %d", code)
	}
	t.Logf("project_http_text_calls=%d binary_calls=%d artifact_byte_match=true secret_redacted=true restart_reused=true revoked_undiscoverable=true public_https_verified=%v", textCalls.Load(), fileCalls.Load(), public)
}
