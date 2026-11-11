package station

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/stationservices"
)

func TestWechatNativePrivateQuoteContinuesOnlyUsedSources(t *testing.T) {
	const account, person, origin, source, unused = "synthetic-account", "wxid-synthetic-owner", "origin@chatroom", "source@chatroom", "unused@chatroom"
	const dataset = "synthetic-dataset"
	now := time.Now().UTC()
	grant := now.Add(-time.Hour)
	connection := wechatapp.ConnectionID(account)
	worker := &stationWechatWorker{dir: t.TempDir(), connectionID: connection}
	owner := stationservices.Owner{FishID: "synthetic-fish", ID: "synthetic-owner"}
	policy := stationWechatPolicy{Grants: map[string]time.Time{person: grant, origin: grant, source: grant, unused: grant}, HistoryGrants: map[string]time.Time{source: grant, unused: grant}}
	savePolicy := func() {
		t.Helper()
		data, err := json.Marshal(policy)
		if err != nil || os.WriteFile(filepath.Join(worker.dir, "worker.json"), data, 0600) != nil {
			t.Fatal("failed to write synthetic policy", err)
		}
	}
	savePolicy()
	if err := worker.saveWechatOwner(stationWechatOwnerState{Account: account, Revision: 1, Binding: &stationWechatOwnerBinding{
		Sender: person, Dataset: dataset, Message: "synthetic-binding", Fish: owner.FishID, Owner: owner.ID, BoundAt: grant,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := worker.saveWechatDirectionGrants(stationWechatCrossGrants{Account: account, Revision: 1, OwnerRevision: 1,
		Fish: owner.FishID, Owner: owner.ID, Sources: policy.HistoryGrants, Origins: map[string]time.Time{origin: grant}, ActivatedAt: grant,
	}, stationWechatCrossGrantsFile); err != nil {
		t.Fatal(err)
	}
	messages := map[string]linuxnative.Message{}
	quotes := map[string]linuxnative.Message{}
	receipts := map[string]linuxnative.ToolStatus{}
	var mu sync.Mutex
	hideReceipt := false
	const api = "/v1/accounts/" + account
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "GET" {
			t.Error("quoted recovery attempted a native write")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var value any
		switch {
		case r.URL.Path == "/v1/status":
			value = map[string]any{"pid": 531, "dataDir": worker.dir, "serverId": dataset, "ownershipGeneration": "synthetic-generation"}
		case r.URL.Path == api+"/sends/by-idempotency-key":
			status, ok := receipts[r.URL.Query().Get("key")]
			if ok && !hideReceipt {
				value = map[string]any{"id": status.ID, "accountId": status.AccountID, "threadId": status.ThreadID,
					"state": status.State, "kind": status.Kind, "serverMessageId": status.ServerID}
			}
		case strings.HasPrefix(r.URL.Path, api+"/messages/"):
			id := strings.TrimPrefix(r.URL.Path, api+"/messages/")
			if message, ok := messages[id]; ok {
				value = message
			} else if message, ok := quotes[strings.TrimSuffix(id, "/quote")]; ok && strings.HasSuffix(id, "/quote") {
				value = linuxnative.QuoteResolution{AccountID: account, ThreadID: message.ThreadID, MessageID: strings.TrimSuffix(id, "/quote"), State: "resolved", Message: &message}
			}
		}
		if value == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	t.Cleanup(daemon.Close)
	file := filepath.Join(worker.dir, "synthetic-token")
	if err := os.WriteFile(file, []byte("synthetic-quote-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: daemon.URL, Account: account, TokenFile: file})
	if err != nil {
		t.Fatal(err)
	}
	newHost := func() *stationWechat {
		return &stationWechat{ctx: t.Context(), stationWechatNative: archiveReadFixtureNative{sender: native},
			workers: map[string]*stationWechatWorker{connection: worker}, ownerReader: func() (stationservices.Owner, error) { return owner, nil }}
	}
	host := newHost()
	memoryPath := filepath.Join(worker.dir, "memory")
	memory, err := companion.NewStore(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if memory != nil {
			_ = memory.Close()
		}
	})
	config := companion.Defaults()
	config.Enabled, config.Permissions.Remember = true, true
	observe := func(id, thread, sender, text string, at time.Time, quote *linuxnative.Message) companion.Input {
		t.Helper()
		m := linuxnative.Message{ID: id, AccountID: account, ThreadID: thread, SenderID: sender, Type: 1, Direction: "incoming", Body: text, ServerID: "native-" + id, CreatedAt: at.Format(time.RFC3339Nano)}
		mu.Lock()
		if quote != nil {
			m.Type, m.Quote = 49, &linuxnative.QuoteReference{ServerID: quote.ServerID, Type: 1}
			quotes[id] = *quote
		}
		messages[id] = m
		mu.Unlock()
		in := companion.Input{Channel: "wechat", Account: account, Thread: thread, Sender: sender, TraceID: id, OriginalText: &text,
			Text: text, Mode: "direct", Directed: true, IsGroup: strings.HasSuffix(thread, "@chatroom"), MessageTime: m.CreatedAt,
			Source: &companion.SourceRef{MessageID: m.ID, ServerID: m.ServerID, MessageType: m.Type, OccurredAt: m.CreatedAt}}
		if quote != nil {
			in.PrivateQuote = quote.ServerID
		}
		if err := memory.Observe(in, config, at); err != nil {
			t.Fatal(err)
		}
		return in
	}
	turn := observe("source-original", source, "synthetic-member", "火车合成原话", now.Add(-10*time.Minute), nil)
	ref, ok := memory.SourceForInput(turn)
	if !ok {
		t.Fatal("missing synthetic original")
	}
	if _, err := memory.SaveMemory(t.Context(), companion.MemoryEntry{Scope: companion.ScopeID("wechat", account, source), Subject: turn.Sender,
		Kind: "fact", Manual: true, Summary: "火车合成摘要", Detail: "火车合成详情", Sources: []companion.MemorySource{{EventID: ref.EventID, Quote: *turn.OriginalText}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	makeTools := func(in companion.Input) *stationAttachmentTools {
		tool := &stationAttachmentTools{crossHost: host, crossConnection: connection, native: native, store: memory, blocked: func() bool { return false }, reads: map[string]time.Time{person: grant}}
		tool.SetTrace(in)
		return tool
	}
	parent := makeTools(observe("request-original", origin, person, "查火车记忆", now.Add(-5*time.Minute), nil))
	if _, err := parent.Call(t.Context(), stationWechatCrossToolName, json.RawMessage(`{"action":"sources"}`)); err != nil {
		t.Fatal(err)
	}
	search := json.RawMessage(`{"action":"search","thread_id":"source@chatroom","query":"火车"}`)
	if result, err := parent.Call(t.Context(), stationWechatCrossToolName, search); err != nil || !strings.Contains(result, "火车合成摘要") {
		t.Fatal(result, err)
	}
	if len(parent.cross.Used) != 1 || parent.cross.Used[0].Thread != source {
		t.Fatal("catalog was mistaken for used source", parent.cross)
	}
	root, err := openWechatCrossDelivery(worker, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	save := func(tool *stationAttachmentTools, partID string, at time.Time, text string) linuxnative.Message {
		t.Helper()
		key := "cross-reply-" + partID
		part := stationWechatCrossDeliveryPart{Key: key + "-0", Text: text, Attempted: true,
			Status: linuxnative.ToolStatus{ID: partID, State: "accepted", Kind: "text", AccountID: account, ThreadID: person, ServerID: strconv.FormatInt(at.Unix(), 10)}}
		mu.Lock()
		defer mu.Unlock()
		receipts[part.Key] = part.Status
		row := stationWechatCrossDelivery{Version: 1, Account: account, Key: key, Origin: tool.trace.Thread, Trace: tool.trace.TraceID,
			Target: person, Created: at, Proofs: tool.cross.Proofs, Used: tool.cross.Used, Parts: []stationWechatCrossDeliveryPart{part}}
		if err := saveWechatCrossDelivery(root, row); err != nil {
			t.Fatal(err)
		}
		m := linuxnative.Message{ID: "outgoing-" + partID, AccountID: account, ThreadID: person, SenderID: "synthetic-agent", Body: text,
			ServerID: part.Status.ServerID, Type: 1, Direction: "outgoing", CreatedAt: at.Format(time.RFC3339Nano)}
		messages[m.ID] = m
		return m
	}
	used := parent.cross.Used
	parent.cross.Used = nil
	legacy := save(parent, "legacy", now.Add(-4*time.Minute-30*time.Second), "旧私聊回复仍受保护")
	parent.cross.Used = used
	legacyQuote := makeTools(observe("request-legacy", person, person, "接着查旧回复", now.Add(-4*time.Minute-20*time.Second), &legacy))
	if _, err := legacyQuote.Call(t.Context(), stationWechatCrossToolName, search); err == nil || !legacyQuote.protectCrossReply(t.Context(), legacy) {
		t.Fatal("old receipt without recorded sources granted access or exposed its body")
	}
	old := save(parent, "first", now.Add(-4*time.Minute), "私聊历史结果不得直接入模")
	current := makeTools(observe("request-next", person, person, "接着查这一条", now.Add(-3*time.Minute), &old))
	if list, err := current.List(t.Context()); err != nil || len(list) != 1 || list[0].Name != stationWechatCrossToolName || current.cross != nil {
		t.Fatal("linked quote discovery loaded content or exposed unrelated tools", list, err)
	}
	if _, allowed, err := current.historyMessage(t.Context(), old.ID); err != nil || allowed {
		t.Fatal("private body readable through ordinary history", allowed, err)
	}
	if result, err := current.Call(t.Context(), stationWechatCrossToolName, json.RawMessage(`{"action":"sources"}`)); err != nil ||
		!strings.Contains(result, source) || strings.Contains(result, unused) || strings.Contains(result, old.Body) || strings.Contains(result, "火车合成") {
		t.Fatal("linked catalog leaked text or included unused scope", result, err)
	}
	if _, err := current.Call(t.Context(), stationWechatCrossToolName, json.RawMessage(`{"action":"search","thread_id":"unused@chatroom","query":"火车"}`)); err == nil {
		t.Fatal("unused source admitted")
	}
	if result, err := current.Call(t.Context(), stationWechatCrossToolName, search); err != nil || !strings.Contains(result, "火车合成摘要") ||
		current.PrivateReplyTarget() != person || !current.PrivateReplyRequired() {
		t.Fatal(result, err)
	}
	linked := save(current, "second", now.Add(-2*time.Minute), "续办回复正文")
	thirdInput := observe("request-third", person, person, "继续", now.Add(-time.Minute), &linked)
	if err := memory.Close(); err != nil {
		t.Fatal(err)
	}
	memory, err = companion.NewStore(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	host = newHost()
	third := makeTools(thirdInput)
	if result, err := third.Call(t.Context(), stationWechatCrossToolName, search); err != nil || !strings.Contains(result, "火车合成摘要") {
		t.Fatal("chained quote lost original authority", result, err)
	}
	foreign := makeTools(observe("foreign-request", person, "other-sender", "继续", now, &linked))
	if _, err := foreign.Call(t.Context(), stationWechatCrossToolName, search); err == nil {
		t.Fatal("unbound sender continued private source")
	}
	mu.Lock()
	hideReceipt = true
	mu.Unlock()
	if _, err := third.Call(t.Context(), stationWechatCrossToolName, search); err == nil || !third.protectCrossReply(t.Context(), linked) {
		t.Fatal("unavailable receipt reopened protected quote")
	}
	mu.Lock()
	hideReceipt = false
	mu.Unlock()
	delete(policy.HistoryGrants, source)
	savePolicy()
	if _, err := third.Call(t.Context(), stationWechatCrossToolName, search); err == nil || !third.protectCrossReply(t.Context(), linked) {
		t.Fatal("revocation reopened protected quote")
	}
	policy.HistoryGrants[source] = grant
	savePolicy()
	if err := memory.Clear(companion.ScopeID("wechat", account, source)); err != nil {
		t.Fatal(err)
	}
	if _, err := makeTools(thirdInput).Call(t.Context(), stationWechatCrossToolName, search); err == nil {
		t.Fatal("forgotten source left linked evidence usable")
	}
}
