package station

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
)

func exerciseStationWechatMemory(t *testing.T, f *localHomeFixture, account string) {
	t.Helper()
	const threadA, threadB = "memory-a@chatroom", "memory-b@chatroom"
	connection := wechatapp.ConnectionID(account)
	base := "/api/apps/wechat/connections/" + connection
	req := func(method, path string, in any, csrf bool, want int) map[string]any {
		t.Helper()
		code, out := f.request(method, path, in, csrf)
		if code != want {
			t.Fatalf("%s %s = %d want %d: %+v", method, path, code, want, out)
		}
		return out
	}
	req("PUT", base+"/policy", map[string]any{"reply_threads": []string{threadA, threadB}, "history_threads": []string{threadA, threadB}}, true, 200)
	grants := req("GET", base+"/memory-grants", nil, false, 200)
	if len(grants["station_reads"].(map[string]any)) != 0 {
		t.Fatal("default cross-source grant open", grants)
	}
	allow := map[string]any{"revision": 0, "request_key": "memory-grant-one", "station_reads": []string{threadA}}
	req("PUT", base+"/memory-grants", allow, false, 403)
	req("PUT", base+"/memory-grants", allow, true, 200)
	req("PUT", base+"/memory-grants", allow, true, 200)
	req("PUT", base+"/memory-grants", map[string]any{"revision": 1, "request_key": "memory-grant-one", "station_reads": []string{threadB}}, true, 409)
	w, err := f.handler.chat.wechat.worker(connection, false)
	if err != nil || w == nil {
		t.Fatal(err)
	}
	memory, err := companion.NewStore(filepath.Join(w.dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	var original companion.MemoryEntry
	for i := range 7 {
		literal := fmt.Sprintf("旅行原话%d", i)
		input := companion.Input{Channel: "wechat", Account: account, Thread: threadA, Sender: "synthetic-owner", Mode: "direct", TraceID: fmt.Sprintf("native-memory-%d", i),
			Text: literal, OriginalText: &literal, MessageTime: time.Now().UTC().Format(time.RFC3339Nano)}
		input.Source = &companion.SourceRef{MessageID: input.TraceID, OccurredAt: input.MessageTime}
		config := companion.Defaults()
		config.Enabled = true
		config.Permissions.Remember = true
		if err := memory.Observe(input, config, time.Now()); err != nil {
			t.Fatal(err)
		}
		source, ok := memory.SourceForInput(input)
		if !ok {
			t.Fatal("synthetic source not recorded")
		}
		entry, err := memory.SaveMemory(t.Context(), companion.MemoryEntry{
			Scope: companion.ScopeID("wechat", account, threadA), Subject: "synthetic-owner", Kind: "fact", Manual: true,
			Summary: fmt.Sprintf("合成旅行摘要%d", i), Detail: fmt.Sprintf("这里是按需读取的旅行原始详情%d。", i),
			Sources: []companion.MemorySource{{EventID: source.EventID, Quote: literal}},
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		original = entry
	}
	direct, err := memory.SearchMemory(t.Context(), companion.MemoryQuery{Scopes: []string{companion.ScopeID("wechat", account, threadA)}, Query: "旅行", Limit: 5})
	if err != nil || len(direct.Items) != 5 {
		t.Fatalf("source fixture search: %+v %v", direct, err)
	}
	if _, err := memory.SaveMemory(t.Context(), companion.MemoryEntry{
		Scope: companion.ScopeID("wechat", account, threadB), Subject: "synthetic-owner", Kind: "fact", Manual: true,
		Summary: "synthetic-group-b-only", Detail: "synthetic-group-b-secret",
	}, 0); err != nil {
		t.Fatal(err)
	}
	newTools := func(trace string) *localToolSet {
		t.Helper()
		store, input, err := f.handler.chat.conversation()
		if err != nil {
			t.Fatal(err)
		}
		text := "查询获准微信会话中的旅行摘要"
		input.TraceID, input.Text, input.OriginalText = trace, text, &text
		input.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
		input.Source = &companion.SourceRef{MessageID: trace}
		if err := store.Observe(input, localConversationConfig(""), time.Now()); err != nil {
			t.Fatal(err)
		}
		run, err := store.OpenLocalToolRun(t.Context(), input, true)
		if err != nil {
			t.Fatal(err)
		}
		tools := f.handler.chat.tools(store, input)
		tools.run = run
		return tools
	}
	tools := newTools("memory-source-request")
	catalog, err := tools.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range catalog {
		found = found || tool.Name == localWechatMemoryTool
	}
	if !found || len(tools.wechatMemoryProofs) != 0 || len(tools.wechatMemoryRefs) != 0 {
		t.Fatal("catalog loaded cross-source memory", found)
	}
	call := func(tool *localToolSet, args any) (string, error) {
		raw, _ := json.Marshal(args)
		return tool.Call(t.Context(), localWechatMemoryTool, raw)
	}
	sources, err := call(tools, map[string]any{"action": "sources"})
	if err != nil || !strings.Contains(sources, threadA) || strings.Contains(sources, threadB) || strings.Contains(sources, "按需读取的原始详情") {
		t.Fatalf("source directory scope leak: %s %v", sources, err)
	}
	search := map[string]any{"action": "search", "connection_id": connection, "account_id": account, "thread_id": threadA, "query": "旅行"}
	raw, err := call(tools, search)
	var page struct {
		Items []struct{ Reference, Summary string }
		Next  string `json:"next_cursor"`
		Limit int    `json:"effective_limit"`
	}
	if err != nil || json.Unmarshal([]byte(raw), &page) != nil || len(page.Items) != 5 || page.Next == "" || page.Limit != 5 ||
		strings.Contains(raw, "旅行原始详情") || strings.Contains(raw, "synthetic-group-b-secret") {
		t.Fatalf("summary-first search failed: %s %v", raw, err)
	}
	ref := page.Items[0].Reference
	if body, err := call(tools, map[string]any{"action": "read", "reference": ref, "length": 36}); err != nil || !strings.Contains(body, "按需读取") {
		t.Fatalf("bound reference read failed: %s %v", body, err)
	}
	next, err := call(tools, map[string]any{"action": "search", "cursor": page.Next, "limit": 1})
	if err != nil || !strings.Contains(next, `"effective_limit":1`) {
		t.Fatalf("explicit page limit lost: %s %v", next, err)
	}
	search["thread_id"] = threadB
	if _, err := call(tools, search); err == nil {
		t.Fatal("ungranted group read succeeded")
	}
	search["thread_id"] = threadA
	search["account_id"] = "two"
	if _, err := call(tools, search); err == nil {
		t.Fatal("another account substituted for connection")
	}
	if _, err := call(newTools("new-request"), map[string]any{"action": "read", "reference": ref}); err == nil {
		t.Fatal("old reference escaped original request")
	}
	modelCalls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			modelCalls++
			raw, _ := json.Marshal(turns)
			switch modelCalls {
			case 1:
				if strings.Contains(string(raw), "合成旅行摘要") || strings.Contains(string(raw), "旅行原始详情") {
					t.Fatal("initial model request preloaded cross-source body")
				}
				body, _ := json.Marshal(map[string]any{"action": "tool", "tool": localWechatMemoryTool, "arguments": map[string]any{"action": "search", "connection_id": connection, "account_id": account, "thread_id": threadA, "query": "旅行", "limit": 1}})
				return string(body), nil
			case 2:
				var reference string
				for _, turn := range turns {
					var packet struct{ Tool, Result string }
					if json.Unmarshal([]byte(turn["content"]), &packet) != nil || packet.Tool != localWechatMemoryTool {
						continue
					}
					var result struct{ Items []struct{ Reference string } }
					if json.Unmarshal([]byte(packet.Result), &result) == nil && len(result.Items) > 0 {
						reference = result.Items[0].Reference
					}
				}
				if reference == "" || strings.Contains(string(raw), "旅行原始详情") {
					t.Fatal("model did not receive summary-only reference")
				}
				body, _ := json.Marshal(map[string]any{"action": "tool", "tool": localWechatMemoryTool, "arguments": map[string]any{"action": "read", "reference": reference}})
				return string(body), nil
			case 3:
				if !strings.Contains(string(raw), "旅行原始详情") {
					t.Fatal("explicit body read absent from model")
				}
				return `{"action":"reply","messages":["这是只供本轮使用的外部证据回答。"]}`, nil
			default:
				if strings.Contains(string(raw), "只供本轮使用的外部证据回答") || strings.Contains(string(raw), "旅行原始详情") {
					t.Fatal("another turn inherited cross-source results")
				}
				return `{"action":"reply","messages":["下一轮正常聊天。"]}`, nil
			}
		}), nil
	}
	if code, out := localChatSend(f, "cross-memory-engine", "查询微信群旅行资料"); code != 200 || out["status"] != "completed" {
		t.Fatalf("normal chat cross-source tool chain failed: %d %+v", code, out)
	}
	if code, out := localChatSend(f, "next-memory-engine", "接着聊别的话题"); code != 200 || out["status"] != "completed" {
		t.Fatalf("next chat failed: %d %+v", code, out)
	}
	if modelCalls != 4 {
		t.Fatalf("unexpected model call count: %d", modelCalls)
	}
	auditFiles, err := os.ReadDir(filepath.Join(w.dir, "memory-query-audit"))
	if err != nil || len(auditFiles) < 2 {
		t.Fatal("cross-source audit absent", err)
	}
	for _, file := range auditFiles {
		raw := mustReadSnapshotFixture(t, filepath.Join(w.dir, "memory-query-audit", file.Name()))
		var audit localWechatMemoryAudit
		if json.Unmarshal(raw, &audit) != nil || audit.EstimatedTokens < 1 || audit.InjectedItems < 1 || audit.Searches < 1 ||
			strings.Contains(string(raw), "原始详情") || strings.Contains(string(raw), "合成旅行摘要") {
			t.Fatalf("cross-source audit retained body or omitted usage: %s", raw)
		}
	}
	if code, history := f.request("GET", "/api/local/chat?limit=30", nil, false); code != 200 {
		t.Fatal(code, history)
	} else {
		raw, _ := json.Marshal(history)
		if !strings.Contains(string(raw), "只供本轮使用的外部证据回答") {
			t.Fatal("external answer disappeared from visible history")
		}
	}
	original.Detail = "管理员更正后的记忆详情"
	if _, err := memory.SaveMemory(t.Context(), original, original.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := call(tools, map[string]any{"action": "read", "reference": ref}); err == nil {
		t.Fatal("corrected source retained old reference")
	}
	if tools.checkAppObservations(t.Context()) == nil {
		t.Fatal("changed source accepted for final response")
	}
	fresh := newTools("after-correction")
	search["account_id"] = account
	if _, err := call(fresh, search); err != nil {
		t.Fatal(err)
	}
	exerciseStationWechatArchive(t, f, w, memory, account, threadA, threadB, newTools)
	req("PUT", base+"/memory-grants", map[string]any{"revision": 1, "request_key": "revoke-memory", "station_reads": []string{}}, true, 200)
	if fresh.checkAppObservations(t.Context()) == nil {
		t.Fatal("revoked scope accepted for final response")
	}
	if _, err := call(tools, map[string]any{"action": "search", "cursor": page.Next}); err == nil {
		t.Fatal("revoked cursor remained valid")
	}
	req("PUT", base+"/memory-grants", map[string]any{"revision": 2, "request_key": "regrant-memory", "station_reads": []string{threadA}}, true, 200)
	req("PUT", base+"/policy", map[string]any{"reply_threads": []string{threadA, threadB}, "history_threads": []string{}}, true, 200)
	if _, err := call(newTools("history-revoked"), search); err == nil {
		t.Fatal("direction grant bypassed history revocation")
	}
}
