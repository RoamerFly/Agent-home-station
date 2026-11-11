package station

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestStationWechatCrossMemoryPrivateDeliveryAndRevocation(t *testing.T) {
	const account, origin, source, ownerID = "one", "origin@chatroom", "source@chatroom", "wxid-synthetic-owner"
	base := t.TempDir()
	data, home := filepath.Join(base, "data"), filepath.Join(base, "home")
	nativeRoot := filepath.Join(data, "apps", "wechat", "native")
	mustWriteSnapshotFixture(t, filepath.Join(nativeRoot, "api-token"), []byte("synthetic-cross-token"))
	var mu sync.Mutex
	messages := map[string]linuxnative.Message{}
	statuses := map[string]map[string]any{}
	sent := []map[string]string{}
	loseNext, hideLookups := false, false
	submitState := "accepted"
	const originalBytes = "synthetic original file bytes"
	const originalName = "旅行计划.txt"
	duplicateGroup, duplicateFile, badHash := false, false, false
	downloads, uploads := 0, 0
	var pauseUpload func()
	newMessage := func(id, thread, sender, text string) linuxnative.Message {
		return linuxnative.Message{ID: id, AccountID: account, ThreadID: thread, SenderID: sender, Body: text, Type: 1, Direction: "incoming", ServerID: "78901", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	messages["bind"] = newMessage("bind", origin, ownerID, "合成本人消息")
	fileMessage := newMessage("original-file", source, "wxid-source-member", "<appmsg><type>6</type><title>旅行计划.txt</title></appmsg>")
	fileMessage.Type = 49
	messages[fileMessage.ID] = fileMessage
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var out any
		switch r.URL.Path {
		case "/v1/status":
			out = map[string]any{"pid": 8472, "dataDir": nativeRoot, "serverId": "synthetic-cross-dataset", "ownershipGeneration": "synthetic-cross-generation"}
		case "/v1/accounts":
			out = map[string]any{"items": []map[string]any{{"id": account, "authenticated": true, "connection": "online"}, {"id": "two", "authenticated": true, "connection": "online"}}}
		case "/v1/accounts/one", "/v1/accounts/two":
			out = map[string]any{"id": filepath.Base(r.URL.Path), "storageMode": "managed", "authenticated": true, "connection": "online"}
		case "/v1/accounts/one/groups":
			group := source
			var next any
			if duplicateGroup {
				if r.URL.Query().Get("cursor") == "" {
					next = "second-group-page"
				} else {
					group = "other-source@chatroom"
				}
			}
			out = map[string]any{"items": []any{map[string]any{"id": group, "name": "远足"}}, "nextCursor": next}
		case "/v1/accounts/one/threads/" + source + "/messages":
			item := fileMessage
			var next any
			if duplicateFile {
				if r.URL.Query().Get("cursor") == "" {
					next = "second-file-page"
				} else {
					item.ID = "duplicate-original"
				}
			}
			out = map[string]any{"items": []any{item}, "nextCursor": next}
		case "/v1/accounts/one/messages/original-file/media-transfers":
			downloads++
			out = map[string]any{"id": "synthetic-download", "state": "completed", "assetId": "synthetic-file-asset", "accountId": account, "kind": "download"}
		case "/v1/accounts/one/media/synthetic-file-asset":
			digest := hexDigest([]byte(originalBytes))
			if badHash {
				digest = hexDigest([]byte("other bytes"))
			}
			out = map[string]any{"id": "synthetic-file-asset", "accountId": account, "messageId": "original-file", "kind": "file", "state": "available", "variant": "original", "actualVariant": "original", "byteLength": len(originalBytes), "contentType": "text/plain", "sha256": digest}
		case "/v1/accounts/one/media/synthetic-file-asset/content":
			_, _ = io.WriteString(w, originalBytes)
			return
		case "/v1/accounts/one/uploads":
			uploads++
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer file.Close()
			bytes, err := io.ReadAll(file)
			if err != nil || string(bytes) != originalBytes || header.Filename != originalName {
				t.Error("forward used different file bytes or title", header.Filename, err)
			}
			out = map[string]any{"id": "synthetic-forward-upload", "name": header.Filename, "byteLength": len(bytes), "sha256": hexDigest(bytes)}
			if pause := pauseUpload; pause != nil {
				pauseUpload = nil
				mu.Unlock()
				pause()
				mu.Lock()
			}
		case "/v1/accounts/one/sends":
			if r.Method != "POST" {
				t.Error("unexpected send method")
				w.WriteHeader(400)
				return
			}
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid synthetic send")
				return
			}
			key := r.Header.Get("Idempotency-Key")
			isText := body["threadId"] == ownerID && body["kind"] == "text"
			isForward := body["threadId"] == origin && body["kind"] == "file" && body["sourceId"] == "synthetic-forward-upload"
			if key == "" || !isText && !isForward {
				t.Error("cross result sent outside private owner", body)
			}
			body["key"] = key
			sent = append(sent, body)
			out = map[string]any{"id": "synthetic-send-" + fmt.Sprint(len(sent)), "accountId": account, "threadId": body["threadId"], "kind": "send", "state": submitState, "serverMessageId": fmt.Sprint(8000 + len(sent)), "sourceId": body["sourceId"]}
			statuses[key] = out.(map[string]any)
			if loseNext {
				loseNext = false
				http.Error(w, "synthetic lost reply", 503)
				return
			}
		case "/v1/accounts/one/sends/by-idempotency-key":
			if hideLookups {
				http.NotFound(w, r)
				return
			}
			var ok bool
			out, ok = statuses[r.URL.Query().Get("key")]
			if !ok {
				http.NotFound(w, r)
				return
			}
		default:
			if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/accounts/one/jobs/") && strings.HasSuffix(r.URL.Path, "/cancel") {
				job := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/accounts/one/jobs/"), "/cancel")
				for _, status := range statuses {
					if status["id"] == job {
						status["state"] = "cancelled"
						_ = json.NewEncoder(w).Encode(status)
						return
					}
				}
				http.NotFound(w, r)
				return
			}
			if !strings.HasPrefix(r.URL.Path, "/v1/accounts/one/messages/") {
				t.Error("unexpected synthetic native request", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			id := strings.TrimPrefix(r.URL.Path, "/v1/accounts/one/messages/")
			var ok bool
			out, ok = messages[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer daemon.Close()
	open := func() *localHomeFixture {
		entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 8472, data)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := apphost.New(entry)
		if err != nil {
			t.Fatal(err)
		}
		h, err := NewLocalHomeHandlerWithApps(data, home, "synthetic-local-password", "", registry)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		t.Cleanup(func() { _ = h.Close() })
		jar, _ := cookiejar.New(nil)
		return &localHomeFixture{t: t, server: srv, client: &http.Client{Jar: jar}, handler: h, dir: data, root: home}
	}
	f := open()
	f.login()
	req := func(method, path string, in any, want int) map[string]any {
		t.Helper()
		code, out := f.request(method, path, in, true)
		if code != want {
			t.Fatalf("%s %s=%d want %d: %+v", method, path, code, want, out)
		}
		return out
	}
	req("POST", "/api/local/home", map[string]string{"action": "initialize"}, 200)
	id := wechatapp.ConnectionID(account)
	path := "/api/apps/wechat/connections/" + id
	preview := req("POST", path+"/owner-binding", map[string]any{"action": "preview", "revision": 0, "message_id": "bind"}, 200)
	req("POST", path+"/owner-binding", map[string]any{"action": "bind", "revision": 0, "message_id": "bind", "digest": preview["digest"], "request_key": "bind-cross-owner", "same_identity": true}, 200)
	policy := map[string]any{"reply_threads": []string{origin, source, ownerID}, "history_threads": []string{source}}
	req("PUT", path+"/policy", policy, 200)
	grant := map[string]any{"revision": 0, "owner_revision": 1, "request_key": "allow-cross-memory", "sources": []string{source}, "origins": []string{origin, ownerID}}
	req("PUT", path+"/cross-memory-grants", grant, 200)
	host := f.handler.chat.wechat
	worker, _ := host.worker(id, false)
	memory, err := companion.NewStore(filepath.Join(worker.dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	config := companion.Defaults()
	config.Enabled = true
	config.QuietStart, config.QuietEnd = "00:00", "00:00"
	config.Permissions.Remember = true
	config.Permissions.ReplyText = true
	config.Permissions.ObserveGroups = true
	var original companion.MemoryEntry
	for n := range 7 {
		text := fmt.Sprintf("旅行原话%d", n)
		input := companion.Input{Channel: "wechat", Account: account, Thread: source, Sender: "synthetic-source-member", TraceID: fmt.Sprintf("source-%d", n), Text: text, OriginalText: &text, MessageTime: time.Now().UTC().Format(time.RFC3339Nano)}
		input.Source = &companion.SourceRef{MessageID: input.TraceID, OccurredAt: input.MessageTime}
		if err := memory.Observe(input, config, time.Now()); err != nil {
			t.Fatal(err)
		}
		ref, ok := memory.SourceForInput(input)
		if !ok {
			t.Fatal("missing source")
		}
		entry, err := memory.SaveMemory(t.Context(), companion.MemoryEntry{Scope: companion.ScopeID("wechat", account, source), Subject: input.Sender, Kind: "fact", Manual: true, Summary: fmt.Sprintf("合成旅行摘要%d", n), Detail: fmt.Sprintf("合成旅行原始详情%d", n), Sources: []companion.MemorySource{{EventID: ref.EventID, Quote: text}}}, 0)
		if err != nil {
			t.Fatal(err)
		}
		original = entry
	}
	makeTools := func(messageID, thread, sender string, words ...string) (*stationAttachmentTools, companion.Input) {
		t.Helper()
		text := "查其他会话的旅行资料"
		if len(words) > 0 {
			text = words[0]
		}
		message := newMessage(messageID, thread, sender, text)
		mu.Lock()
		messages[messageID] = message
		mu.Unlock()
		in := companion.Input{Channel: "wechat", Account: account, Thread: thread, Sender: sender, TraceID: messageID, Text: text, OriginalText: &text, MessageTime: message.CreatedAt, IsGroup: strings.HasSuffix(thread, "@chatroom"), Mode: "direct", Directed: true}
		in.Source = &companion.SourceRef{MessageID: in.TraceID, ServerID: message.ServerID, Account: account, Thread: thread, Sender: sender, MessageType: 1, OccurredAt: in.MessageTime}
		if err := memory.Observe(in, config, time.Now()); err != nil {
			t.Fatal(err)
		}
		tools := &stationAttachmentTools{crossHost: host, crossConnection: id, store: memory, native: nil, blocked: func() bool { return false }}
		tools.native, err = host.WorkerSender(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		tools.SetTrace(in)
		return tools, in
	}
	call := func(tools *stationAttachmentTools, args any) (string, error) {
		raw, _ := json.Marshal(args)
		return tools.Call(t.Context(), stationWechatCrossToolName, raw)
	}
	tools, input := makeTools("query-one", origin, ownerID)
	catalog, err := tools.List(t.Context())
	if err != nil || len(catalog) != 1 || catalog[0].Name != stationWechatCrossToolName || tools.cross != nil {
		t.Fatal("discovery preloaded foreign data", catalog, err)
	}
	stamp := tools.MemoryResultAuthorization()
	result, err := call(tools, map[string]any{"action": "search", "thread_id": source, "query": "旅行"})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []struct{ Reference string }
		Next  string `json:"next_cursor"`
		Limit int    `json:"effective_limit"`
	}
	if json.Unmarshal([]byte(result), &page) != nil || len(page.Items) != 5 || page.Limit != 5 || strings.Contains(result, "原始详情") || !tools.PrivateReplyRequired() || tools.PrivateReplyTarget() != ownerID || !tools.MemoryResultAuthorized(stamp) {
		t.Fatal(result)
	}
	read, err := call(tools, map[string]any{"action": "read", "reference": page.Items[0].Reference})
	if err != nil || !strings.Contains(read, "原始详情") {
		t.Fatal(read, err)
	}
	if _, err := call(tools, map[string]any{"action": "search", "cursor": page.Next, "limit": 1}); err != nil {
		t.Fatal(err)
	}
	fresh := tools.ForkTools().(*stationAttachmentTools)
	fresh.SetTrace(input)
	if _, err := call(fresh, map[string]any{"action": "read", "reference": page.Items[0].Reference}); err == nil {
		t.Fatal("reference crossed requests")
	}
	member, _ := makeTools("member-query", origin, "wxid-member")
	if list, err := member.List(t.Context()); err != nil || len(list) != 0 {
		t.Fatal("ordinary member got cross tool", list, err)
	}
	if _, err := call(member, map[string]any{"action": "search", "thread_id": source, "query": "旅行"}); err == nil {
		t.Fatal("member impersonation")
	}
	if _, err := call(fresh, map[string]any{"action": "search", "thread_id": "ungranted@chatroom", "query": "旅行"}); err == nil {
		t.Fatal("ungranted source exposed")
	}
	// Waiting for the main worker lock during an authority check would deadlock
	// a drain operation. Runtime checks use the atomic private policy snapshot.
	worker.mu.Lock()
	done := make(chan error, 1)
	go func() { _, err := tools.crossProof(t.Context(), tools.cross.Proofs[0]); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		worker.mu.Unlock()
		t.Fatal("runtime authority waited for worker main lock")
	}
	worker.mu.Unlock()
	if err := tools.RoutePrivateReply(t.Context(), []string{"合成私聊结果一", "合成私聊结果二"}); err != nil {
		t.Fatal(err)
	}
	if err := tools.RoutePrivateReply(t.Context(), []string{"重试的新措辞不能覆盖原文"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(sent) != 2 || sent[0]["text"] != "合成私聊结果一" || sent[1]["text"] != "合成私聊结果二" {
		t.Fatal(sent)
	}
	mu.Unlock()
	key := "cross-reply-" + localCheckpointKey(memory.SourceInstanceID(), account, origin, input.TraceID, ownerID)[:40]
	receipt, err := host.ReadReceipt(t.Context(), id, key)
	if err != nil || receipt.State != "accepted" || len(receipt.AcceptedParts) != 2 {
		t.Fatal(receipt, err)
	}
	if b, _ := json.Marshal(memory.Context(input, config, time.Now())); strings.Contains(string(b), "合成私聊结果") {
		t.Fatal("private answer entered group memory")
	}
	// An unacknowledged native submission remains query-only across a complete
	// Station host reconstruction, with the original frozen text.
	lost, lostInput := makeTools("lost-query", origin, ownerID)
	if _, err := call(lost, map[string]any{"action": "search", "thread_id": source, "query": "旅行"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	loseNext = true
	mu.Unlock()
	if err := lost.RoutePrivateReply(t.Context(), []string{"丢失回执的原文"}); err == nil {
		t.Fatal("lost receipt claimed success")
	}
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	f.login()
	host = f.handler.chat.wechat
	restarted := &stationAttachmentTools{crossHost: host, crossConnection: id, store: memory, blocked: func() bool { return false }}
	restarted.native, err = host.WorkerSender(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	restarted.SetTrace(lostInput)
	if _, err := call(restarted, map[string]any{"action": "search", "thread_id": source, "query": "旅行"}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RoutePrivateReply(t.Context(), []string{"重启后新措辞"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(sent) != 3 || sent[2]["text"] != "丢失回执的原文" {
		t.Fatal("restart resubmitted", sent)
	}
	mu.Unlock()
	unknown, _ := makeTools("unknown-query", origin, ownerID)
	if _, err := call(unknown, map[string]any{"action": "search", "thread_id": source, "query": "旅行"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	loseNext = true
	mu.Unlock()
	_ = unknown.RoutePrivateReply(t.Context(), []string{"未知结果原文"})
	mu.Lock()
	hideLookups = true
	mu.Unlock()
	if err := unknown.RoutePrivateReply(t.Context(), []string{"不能重发"}); err == nil {
		t.Fatal("missing original receipt silently accepted")
	}
	mu.Lock()
	if len(sent) != 4 {
		t.Fatal("unknown request replayed", sent)
	}
	hideLookups = false
	mu.Unlock()
	oldStamp := restarted.MemoryResultAuthorization()
	original.Summary += "（已更正）"
	if _, err := memory.SaveMemory(t.Context(), original, original.Version); err != nil {
		t.Fatal(err)
	}
	if restarted.MemoryResultAuthorized(oldStamp) || restarted.checkCrossResults(t.Context()) == nil {
		t.Fatal("source correction preserved old query evidence")
	}
	// Both group and private owner requests use the actual model/tool loop and
	// keep cross-source bodies out of subsequent ordinary model context.
	for _, thread := range []string{origin, ownerID} {
		engineTools, engineInput := makeTools("engine-query-"+fmt.Sprint(thread == ownerID), thread, ownerID)
		calls := 0
		model := localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			calls++
			raw, _ := json.Marshal(turns)
			switch calls {
			case 1:
				if strings.Contains(string(raw), "合成旅行摘要") || strings.Contains(string(raw), "合成旅行原始详情") {
					t.Fatal("ordinary first model input preloaded foreign memory")
				}
				args, _ := json.Marshal(map[string]any{"action": "tool", "tool": stationWechatCrossToolName, "arguments": map[string]any{"action": "search", "thread_id": source, "query": "旅行", "limit": 1}})
				return string(args), nil
			case 2:
				if strings.Contains(string(raw), "合成旅行原始详情") {
					t.Fatal("search exposed unrequested full detail")
				}
				var reference string
				for _, turn := range turns {
					var packet struct{ Tool, Result string }
					if json.Unmarshal([]byte(turn["content"]), &packet) != nil || packet.Tool != stationWechatCrossToolName {
						continue
					}
					var result struct{ Items []struct{ Reference string } }
					if json.Unmarshal([]byte(packet.Result), &result) == nil && len(result.Items) > 0 {
						reference = result.Items[0].Reference
					}
				}
				if reference == "" {
					t.Fatal("model missing summary reference", string(raw))
				}
				args, _ := json.Marshal(map[string]any{"action": "tool", "tool": stationWechatCrossToolName, "arguments": map[string]any{"action": "read", "reference": reference}})
				return string(args), nil
			case 3:
				if !strings.Contains(string(raw), "合成旅行原始详情") {
					t.Fatal("model did not receive explicitly read detail")
				}
				return `{"action":"reply","messages":["本轮专用的合成跨群回答"]}`, nil
			default:
				if strings.Contains(string(raw), "合成旅行原始详情") || strings.Contains(string(raw), "本轮专用的合成跨群回答") {
					t.Fatal("next model turn inherited foreign context")
				}
				return `{"action":"reply","messages":["继续当前会话"]}`, nil
			}
		})
		engine, err := companion.New(config, memory, model, "合成微信跨群回放")
		if err != nil {
			t.Fatal(err)
		}
		engine.Tools = engineTools
		engine.MaxToolCalls = 6
		decision, err := engine.Respond(t.Context(), engineInput)
		if err != nil || decision.Action != "silent" || decision.Reason != "cross_conversation_result_private_delivery_handled" || len(decision.Messages) != 0 || calls != 3 {
			t.Fatalf("engine private route: %+v calls=%d %v", decision, calls, err)
		}
		_, next := makeTools("engine-next-"+fmt.Sprint(thread == ownerID), thread, ownerID)
		decision, err = engine.Respond(t.Context(), next)
		if err != nil || decision.Action != "reply" || calls != 4 {
			t.Fatal("ordinary next turn", decision, calls, err)
		}
	}
	// New private-route receipts share the application's normal receipt list.
	seenReceipts := map[string]bool{}
	after := ""
	for n := 0; n < 20; n++ {
		page, err := host.ListReceipts(t.Context(), id, after, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatal("private route receipt page", page, err)
		}
		k := page.Items[0].IdempotencyKey
		if seenReceipts[k] {
			t.Fatal("duplicate receipt page", k)
		}
		seenReceipts[k] = true
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "合成私聊结果") || strings.Contains(string(raw), "合成旅行原始详情") {
			t.Fatal("receipt list exposed private body")
		}
		after = page.NextAfter
		if after == "" {
			break
		}
	}
	if !seenReceipts[key] || len(seenReceipts) != 5 {
		t.Fatal("receipt list lost private route", seenReceipts)
	}
	if _, err := host.ReadReceipt(t.Context(), wechatapp.ConnectionID("two"), key); err == nil {
		t.Fatal("receipt crossed accounts")
	}
	cancelTools, cancelInput := makeTools("cancel-private-query", origin, ownerID)
	if _, err := call(cancelTools, map[string]any{"action": "search", "thread_id": source, "query": "旅行"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	submitState = "queued"
	mu.Unlock()
	if err := cancelTools.RoutePrivateReply(t.Context(), []string{"待取消的合成私聊"}); err != nil {
		t.Fatal(err)
	}
	cancelKey := "cross-reply-" + localCheckpointKey(memory.SourceInstanceID(), account, origin, cancelInput.TraceID, ownerID)[:40]
	if before, err := host.ReadReceipt(t.Context(), id, cancelKey); err != nil || before.State == "accepted" {
		t.Fatal("queued receipt claimed delivery", before, err)
	}
	req("POST", path+"/receipts/"+cancelKey+"/cancel", map[string]any{}, 202)
	if stopped, err := host.ReadReceipt(t.Context(), id, cancelKey); err != nil || !stopped.CancelRequested || stopped.Deliveries[0].State != "cancelled" {
		t.Fatal("private cancellation not recorded", stopped, err)
	}
	mu.Lock()
	submitState = "accepted"
	mu.Unlock()
	_, currentInput := makeTools("current-proof-query", origin, ownerID)
	current := &stationAttachmentTools{crossHost: host, crossConnection: id, store: memory, native: restarted.native, blocked: func() bool { return false }}
	current.SetTrace(currentInput)
	if _, err := call(current, map[string]any{"action": "search", "thread_id": source, "query": "旅行"}); err != nil {
		t.Fatal(err)
	}
	currentStamp := current.MemoryResultAuthorization()
	policy["reply_threads"] = []string{origin, source}
	req("PUT", path+"/policy", policy, 200)
	blocked, _ := makeTools("no-private-query", origin, ownerID)
	if _, err := call(blocked, map[string]any{"action": "search", "thread_id": source, "query": "旅行"}); err == nil || len(blocked.cross.Refs) != 0 || blocked.PrivateReplyUnavailableNotice() == "" {
		t.Fatal("missing private route exposed evidence")
	}
	blockedTools, blockedInput := makeTools("blocked-engine", origin, ownerID)
	blockedCalls := 0
	blockedModel := localModelFunc(func(_ context.Context, _ string, _ []map[string]string) (string, error) {
		blockedCalls++
		if blockedCalls == 1 {
			args, _ := json.Marshal(map[string]any{"action": "tool", "tool": stationWechatCrossToolName, "arguments": map[string]any{"action": "search", "thread_id": source, "query": "旅行"}})
			return string(args), nil
		}
		return `{"action":"reply","messages":["模型草稿不应回到群里"]}`, nil
	})
	blockedEngine, err := companion.New(config, memory, blockedModel, "合成私聊不可用")
	if err != nil {
		t.Fatal(err)
	}
	blockedEngine.Tools = blockedTools
	blockedEngine.MaxToolCalls = 6
	blockedDecision, err := blockedEngine.Respond(t.Context(), blockedInput)
	if err != nil || blockedDecision.Action != "reply" || len(blockedDecision.Messages) != 1 || blockedDecision.Messages[0] != blockedTools.PrivateReplyUnavailableNotice() {
		t.Fatal("group fallback exposed model text", blockedDecision, err)
	}
	if current.MemoryResultAuthorized(currentStamp) {
		t.Fatal("old result survived private revocation")
	}
	grant["revision"], grant["request_key"], grant["sources"], grant["origins"] = 1, "revoke-cross-memory", []string{}, []string{}
	req("PUT", path+"/cross-memory-grants", grant, 200)
	if _, err := call(restarted, map[string]any{"action": "read", "reference": page.Items[0].Reference}); err == nil {
		t.Fatal("revoked source readable")
	}
	if receipt, err := host.ReadReceipt(t.Context(), id, key); err != nil || receipt.State != "accepted" {
		t.Fatal("revocation lost existing native receipt", receipt, err)
	}

	// The current group explicitly receives only the selected original. Its
	// permission is independent of the owner's private reply destination.
	policy["attachment_threads"] = []string{origin, source}
	policy["media_reply_threads"] = []string{origin}
	req("PUT", path+"/policy", policy, 200)
	grant["revision"], grant["request_key"], grant["sources"], grant["origins"] = 2, "authorize-forward-source", []string{source}, []string{origin}
	req("PUT", path+"/cross-memory-grants", grant, 200)
	forward := func(tools *stationAttachmentTools, args string, want string) apphost.Receipt {
		t.Helper()
		raw, err := tools.Call(t.Context(), stationWechatForwardToolName, json.RawMessage(args))
		if want == "denied" {
			if err == nil {
				t.Fatal("unsafe forward accepted", raw)
			}
			return apphost.Receipt{}
		}
		var result apphost.Receipt
		if err != nil || json.Unmarshal([]byte(raw), &result) != nil || result.State != want {
			t.Fatalf("forward want %s, got %s: %v", want, raw, err)
		}
		return result
	}
	words := "把群「远足」的「旅行计划.txt」转到这个群"
	exact := "转发原件 " + source + " original-file 到这个群"
	newForward := func(trace, sender, text string) *stationAttachmentTools {
		tools, _ := makeTools(trace, origin, sender, text)
		return tools
	}
	forward(newForward("wrong-speaker", "wxid-other", words), `{}`, "denied")
	forward(newForward("wrong-intent", ownerID, "看看群远足的旅行计划.txt"), `{}`, "denied")
	forward(newForward("wrong-target", ownerID, words), `{"target_thread_id":"other@chatroom"}`, "denied")
	foreign, _ := makeTools("foreign-thread", origin, ownerID, exact)
	foreign.trace.Account = "two"
	forward(foreign, `{}`, "denied")
	mu.Lock()
	duplicateGroup = true
	mu.Unlock()
	forward(newForward("duplicate-group", ownerID, words), `{}`, "needs_clarification")
	mu.Lock()
	duplicateGroup = false
	duplicateFile = true
	mu.Unlock()
	forward(newForward("duplicate-file", ownerID, words), `{}`, "needs_clarification")
	mu.Lock()
	duplicateFile = false
	badHash = true
	mu.Unlock()
	forward(newForward("bad-original", ownerID, exact), `{}`, "denied")
	mu.Lock()
	badHash = false
	if downloads != 1 || uploads != 0 {
		t.Fatal("ambiguity or bad original reached upload", downloads, uploads)
	}
	beforeSends := len(sent)
	loseNext = true
	mu.Unlock()
	first, firstInput := makeTools("lost-forward-reply", origin, ownerID, words)
	listed, err := first.List(t.Context())
	if err != nil || len(listed) != 2 || listed[1].Name != stationWechatForwardToolName {
		t.Fatal("explicit command tool absent", listed, err)
	}
	unknownReceipt := forward(first, `{}`, "unknown")
	mu.Lock()
	hideLookups = true
	mu.Unlock()
	retry := first.ForkTools().(*stationAttachmentTools)
	retry.SetTrace(firstInput)
	forward(retry, `{}`, "unknown")
	mu.Lock()
	hideLookups = false
	mu.Unlock()
	accepted := forward(retry, `{}`, "accepted")
	if accepted.IdempotencyKey != unknownReceipt.IdempotencyKey || accepted.ConversationID != origin || accepted.Kind != "file" || accepted.ServerID == "" {
		t.Fatal(accepted)
	}
	mu.Lock()
	if len(sent) != beforeSends+1 || uploads != 1 {
		t.Fatal("result unknown caused duplicate transfer", len(sent), uploads)
	}
	mu.Unlock()
	fileReceipt, err := host.ReadReceipt(t.Context(), id, accepted.IdempotencyKey)
	if err != nil || fileReceipt.State != "accepted" || fileReceipt.Kind != "file" {
		t.Fatal(fileReceipt, err)
	}
	queued := newForward("cancel-forward", ownerID, exact)
	mu.Lock()
	submitState = "queued"
	mu.Unlock()
	queuedReceipt := forward(queued, `{}`, "queued")
	req("POST", path+"/receipts/"+queuedReceipt.IdempotencyKey+"/cancel", map[string]any{}, 202)
	stopped, err := host.ReadReceipt(t.Context(), id, queuedReceipt.IdempotencyKey)
	if err != nil || !stopped.CancelRequested || stopped.State != "cancelled" {
		t.Fatal(stopped, err)
	}
	engineTools, engineInput := makeTools("explicit-forward-engine", origin, ownerID, words)
	var modelCalls int
	forwardModel := localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		modelCalls++
		if modelCalls == 1 {
			return `{"action":"tool","tool":"wechat_forward_file","arguments":{}}`, nil
		}
		for _, turn := range turns {
			var packet struct{ Tool, Result string }
			if json.Unmarshal([]byte(turn["content"]), &packet) == nil && packet.Tool == stationWechatForwardToolName {
				var result apphost.Receipt
				if json.Unmarshal([]byte(packet.Result), &result) == nil && result.State == "accepted" && result.ConversationID == origin && result.ServerID != "" {
					return `{"action":"silent","reason":"already_delivered"}`, nil
				}
			}
		}
		return "", fmt.Errorf("tool result missing verified forward receipt")
	})
	mu.Lock()
	submitState = "accepted"
	mu.Unlock()
	engine, err := companion.New(config, memory, forwardModel, "合成微信转发回放")
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools, engine.MaxToolCalls = engineTools, 6
	if decision, err := engine.Respond(t.Context(), engineInput); err != nil || decision.Action != "silent" || modelCalls != 2 {
		t.Fatal(decision, modelCalls, err)
	}

	// Revocation while the native upload response is in flight must stop the
	// dispatch even if the upload itself finishes successfully.
	inFlight := newForward("revoked-during-upload", ownerID, exact)
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	mu.Lock()
	pauseUpload = func() { close(started); <-release }
	mu.Unlock()
	dispatchResult := make(chan error, 1)
	go func() {
		_, err := inFlight.Call(t.Context(), stationWechatForwardToolName, json.RawMessage(`{}`))
		dispatchResult <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start")
	}
	// Receipt reads must stay available while a transfer is in flight.
	inFlightKey := "cross-forward-" + localCheckpointKey(account, origin, "revoked-during-upload")[:40]
	receiptContext, cancelReceipt := context.WithTimeout(t.Context(), 2*time.Second)
	pendingReceipt, err := host.ReadReceipt(receiptContext, id, inFlightKey)
	cancelReceipt()
	if err != nil || pendingReceipt.State != "prepared" {
		t.Fatal("in-flight receipt unavailable", pendingReceipt, err)
	}
	blockedForward := newForward("revoked-forward-target", ownerID, exact)
	policy["media_reply_threads"] = []string{}
	req("PUT", path+"/policy", policy, 200)
	close(release)
	if err := <-dispatchResult; err == nil {
		t.Fatal("revocation during upload allowed submit")
	}
	forward(blockedForward, `{}`, "denied")
	mu.Lock()
	if len(sent) != beforeSends+3 || uploads != 4 {
		t.Fatal("revoked target wrote to native", len(sent), uploads)
	}
	mu.Unlock()
}
