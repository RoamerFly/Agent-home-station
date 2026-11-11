package station

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func checkWechatCrossArchive(t *testing.T, f *localHomeFixture, base, origin, source, ownerID string, makeTools func(string, string, string, ...string) (*stationAttachmentTools, companion.Input), config companion.Config) {
	t.Helper()
	const question = "请查另一个群封存的星港记忆"
	host := f.handler.chat.wechat
	connection := strings.TrimPrefix(base, "/api/apps/wechat/connections/")
	worker, err := host.worker(connection, false)
	if err != nil || worker == nil {
		t.Fatal(err)
	}
	worker.ownerMu.Lock()
	bundle := stationLegacyMemory{Version: 1, Account: "one", Thread: source, Grant: worker.policy.HistoryGrants[source], Digest: hexDigest([]byte("synthetic cross archive"))}
	worker.ownerMu.Unlock()
	at := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	for i := range 7 {
		text := fmt.Sprintf("封存星港原话%d", i) + strings.Repeat("甲", 300) + "跨群原话页尾"
		bundle.Selection.Events = append(bundle.Selection.Events, companion.LegacyMemoryEvent{InstanceID: "cross-archive-synthetic", EventID: fmt.Sprint("cross-old-event-", i),
			MessageID: fmt.Sprint("cross-old-message-", i), Sender: "old-source-member", ServerID: "34567", OccurredAt: at, Revision: 1, Text: text, Hash: hexDigest([]byte(text))})
	}
	bundle.Selection.Memories = []companion.LegacyMemoryFact{{ID: "cross-old-fact", Version: 1, Kind: "fact", Text: "封存星港摘要" + strings.Repeat("乙", 300) + "跨群派生正文页尾",
		Sources: []companion.LegacyMemoryFactSource{{EventID: bundle.Selection.Events[0].EventID, Revision: 1}}}}
	write := func(item stationLegacyMemory) {
		t.Helper()
		raw, _ := json.Marshal(item)
		mustWriteSnapshotFixture(t, filepath.Join(worker.dir, stationLegacyMemoryFile(item.Account, item.Thread)), raw)
	}
	write(bundle)
	call := func(tools *stationAttachmentTools, arguments map[string]any) (string, error) {
		t.Helper()
		raw, _ := json.Marshal(arguments)
		return tools.Call(t.Context(), stationWechatCrossToolName, raw)
	}
	deny := func(tools *stationAttachmentTools, args map[string]any) {
		t.Helper()
		if raw, err := call(tools, args); err == nil {
			t.Fatal("unauthorized archive result", raw)
		}
	}
	tools, input := makeTools("archive-cross-query", origin, ownerID, question)
	query := map[string]any{"action": "search", "thread_id": source, "dataset": "archive", "query": "封存星港"}
	if _, err := tools.List(t.Context()); err != nil || tools.cross != nil {
		t.Fatal("discovery loaded source text", err)
	}
	directory, err := call(tools, map[string]any{"action": "sources"})
	var listing struct {
		Archives []string `json:"archive_sources"`
	}
	if err != nil || json.Unmarshal([]byte(directory), &listing) != nil || len(listing.Archives) != 1 || listing.Archives[0] != source ||
		strings.Contains(directory, "封存星港摘要") {
		t.Fatal(directory, err)
	}
	member, _ := makeTools("archive-cross-member", origin, "wxid-member", question)
	deny(member, query)
	foreign := tools.ForkTools().(*stationAttachmentTools)
	foreignInput := input
	foreignInput.Account = "two"
	foreign.SetTrace(foreignInput)
	deny(foreign, query)
	ungranted, _ := makeTools("archive-cross-other-source", origin, ownerID, question)
	deny(ungranted, map[string]any{"action": "search", "thread_id": "ungranted@chatroom", "dataset": "archive", "query": "封存星港"})
	raw, err := call(tools, query)
	var page struct {
		Items []struct{ Reference string }
		Next  string `json:"next_cursor"`
		Limit int    `json:"effective_limit"`
	}
	if err != nil || json.Unmarshal([]byte(raw), &page) != nil || len(page.Items) != 5 || page.Next == "" || page.Limit != 5 ||
		strings.Contains(raw, "跨群派生正文页尾") || strings.Contains(raw, "跨群原话页尾") || !tools.PrivateReplyRequired() || tools.PrivateReplyTarget() != ownerID {
		t.Fatal("archive summary or private route", raw, err)
	}
	ref := page.Items[0].Reference
	if raw, err := call(tools, map[string]any{"action": "read", "reference": ref}); err != nil || !strings.Contains(raw, "跨群派生正文页尾") ||
		strings.Contains(raw, "跨群原话页尾") {
		t.Fatal("derived memory read", raw, err)
	}
	originalPart, err := call(tools, map[string]any{"action": "read", "reference": ref, "source_index": 0, "length": 48})
	if err != nil ||
		!strings.Contains(originalPart, `"has_more":true`) || strings.Contains(originalPart, "跨群原话页尾") {
		t.Fatal("original byte page", originalPart, err)
	}
	var originalPage struct {
		Next int `json:"next_offset"`
	}
	if json.Unmarshal([]byte(originalPart), &originalPage) != nil {
		t.Fatal(originalPart)
	}
	if raw, err := call(tools, map[string]any{"action": "read", "reference": ref, "source_index": 0, "offset": originalPage.Next}); err != nil ||
		!strings.Contains(raw, "跨群原话页尾") {
		t.Fatal("original continuation", raw, err)
	}
	if raw, err := call(tools, map[string]any{"action": "search", "cursor": page.Next, "limit": 1}); err != nil ||
		!strings.Contains(raw, `"effective_limit":1`) {
		t.Fatal("archive search continuation", raw, err)
	}
	another := tools.ForkTools().(*stationAttachmentTools)
	another.SetTrace(input)
	deny(another, map[string]any{"action": "read", "reference": ref})
	deny(another, map[string]any{"action": "search", "cursor": page.Next})
	originals, err := tools.store.SearchMemory(t.Context(), companion.MemoryQuery{Scopes: []string{companion.ScopeID("wechat", input.Account, source)}, Query: "封存星港", Originals: true})
	if err != nil || len(originals.Items) != 0 {
		t.Fatal("query imported archived originals into live memory", originals, err)
	}
	for _, thread := range []string{origin, ownerID} {
		current, in := makeTools("archive-cross-engine-"+thread, thread, ownerID, question)
		calls := 0
		model := localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			calls++
			history := localModelHistory(turns)
			if calls == 1 {
				if strings.Contains(history, "封存星港摘要") || strings.Contains(history, "封存星港原话") {
					t.Fatal("first model input preloaded archive contents")
				}
				raw, _ := json.Marshal(map[string]any{"action": "tool", "tool": stationWechatCrossToolName, "arguments": query})
				return string(raw), nil
			}
			if !strings.Contains(history, "封存星港摘要") || strings.Contains(history, "跨群原话页尾") {
				t.Fatal("model received incorrect archive coverage", history)
			}
			return `{"action":"reply","messages":["合成封存资料私下回传"]}`, nil
		})
		engine, err := companion.New(config, current.store, model, "封存资料按需查询回放")
		if err != nil {
			t.Fatal(err)
		}
		engine.Tools, engine.MaxToolCalls = current, 6
		decision, err := engine.Respond(t.Context(), in)
		if err != nil || calls != 2 || decision.Action != "silent" || decision.Reason != "cross_conversation_result_private_delivery_handled" {
			t.Fatal(decision, calls, err)
		}
		for _, turn := range current.store.Context(in, config, time.Now()).Messages {
			if strings.Contains(turn.Text, "合成封存资料私下回传") || strings.Contains(turn.Text, "封存星港摘要") {
				t.Fatal("cross-conversation result entered ordinary context", turn)
			}
		}
	}
	// Exercise the real persistent delivery record, then restore a tool turn
	// against the same native request after the source archive changes.
	if err := tools.RoutePrivateReply(t.Context(), []string{"不可替换的封存资料回复"}); err != nil {
		t.Fatal(err)
	}
	key := "cross-reply-" + localCheckpointKey(tools.store.SourceInstanceID(), input.Account, input.Thread, input.TraceID, ownerID)[:40]
	rowDir, err := openWechatCrossDelivery(worker, false)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := readWechatCrossDelivery(rowDir, key, input.Account)
	rowDir.Close()
	if err != nil || len(stored.Proofs) == 0 {
		t.Fatal("durable archive proof missing", err)
	}
	archived := false
	for _, proof := range stored.Proofs {
		if proof.Archived && proof.ArchiveFingerprint == "" {
			t.Fatal("source fingerprint was omitted from persisted proof")
		}
		archived = archived || proof.Archived
	}
	if !archived {
		t.Fatal("delivery lost its archived source proof")
	}
	changed := bundle
	changed.Selection.Memories = append([]companion.LegacyMemoryFact{}, bundle.Selection.Memories...)
	changed.Selection.Memories[0].Text = "封存星港的新派生记忆"
	write(changed)
	deny(tools, map[string]any{"action": "read", "reference": ref})
	deny(tools, map[string]any{"action": "search", "cursor": page.Next})
	restored := tools.ForkTools().(*stationAttachmentTools)
	restored.SetTrace(input)
	if _, err := call(restored, query); err != nil {
		t.Fatal("fresh query after replacement", err)
	}
	if restored.RoutePrivateReply(t.Context(), []string{"新措辞不能覆盖原账本"}) == nil {
		t.Fatal("restored delivery reused a changed archive")
	}
	if code, receipt := f.request("GET", base+"/receipts/"+key, nil, false); code != 200 || receipt["state"] != "accepted" {
		t.Fatal("source replacement hid the accepted native receipt", code, receipt)
	}
	write(bundle)
	corrected, _ := makeTools("archive-cross-corrected", origin, ownerID, question)
	if _, err := call(corrected, query); err != nil {
		t.Fatal(err)
	}
	event := bundle.Selection.Events[0]
	text := "当前已更正的来源消息"
	correction := companion.Input{Channel: "wechat", Account: input.Account, Thread: source, Sender: event.Sender, TraceID: event.MessageID,
		Text: text, OriginalText: &text, MessageTime: event.OccurredAt, Source: &companion.SourceRef{MessageID: event.MessageID, ServerID: event.ServerID, Revision: 2, OccurredAt: event.OccurredAt}}
	if err := tools.store.Observe(correction, config, time.Now()); err != nil {
		t.Fatal(err)
	}
	if corrected.checkCrossResults(t.Context()) == nil || corrected.RoutePrivateReply(t.Context(), []string{"旧来源已更正"}) == nil {
		t.Fatal("source correction left old evidence deliverable")
	}
	fresh, _ := makeTools("archive-cross-after-correction", origin, ownerID, question)
	if raw, err := call(fresh, query); err != nil || strings.Contains(raw, "封存星港摘要") || strings.Contains(raw, "封存星港原话0") {
		t.Fatal("fresh query bypassed corrected source", raw, err)
	}
	if err := tools.store.Clear(companion.ScopeID("wechat", input.Account, source)); err != nil {
		t.Fatal(err)
	}
	cleared, _ := makeTools("archive-cross-cleared", origin, ownerID, question)
	deny(cleared, query)
}
