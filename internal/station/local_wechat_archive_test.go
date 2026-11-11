package station

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func exerciseStationWechatArchive(t *testing.T, f *localHomeFixture, worker *stationWechatWorker, live *companion.Store, account, thread, denied string, newTools func(string) *localToolSet) {
	t.Helper()
	const palace = "/api/local/memory/palace"
	grant := worker.policy.HistoryGrants[thread]
	bundle := stationLegacyMemory{Version: 1, Account: account, Thread: thread, Grant: grant, Digest: hexDigest([]byte("synthetic archive proof"))}
	at := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	for i := range 7 {
		text := fmt.Sprintf("星港原始消息%d", i) + strings.Repeat("甲", 300) + "历史原文页尾"
		bundle.Selection.Events = append(bundle.Selection.Events, companion.LegacyMemoryEvent{InstanceID: "legacy-synthetic", EventID: fmt.Sprint("old-event-", i),
			MessageID: fmt.Sprint("old-message-", i), Sender: "old-synthetic-member", ServerID: "23456", OccurredAt: at, Revision: 1, Text: text, Hash: hexDigest([]byte(text))})
	}
	bundle.Selection.Memories = []companion.LegacyMemoryFact{{ID: "old-fact", Version: 1, Kind: "fact", Text: "星港旧摘要" + strings.Repeat("乙", 300) + "封存派生正文末尾",
		Sources: []companion.LegacyMemoryFactSource{{EventID: bundle.Selection.Events[0].EventID, Revision: 1}}}}
	path := filepath.Join(worker.dir, stationLegacyMemoryFile(account, thread))
	write := func(bundle stationLegacyMemory) {
		t.Helper()
		raw, _ := json.Marshal(bundle)
		mustWriteSnapshotFixture(t, path, raw)
	}
	write(bundle)
	get := func(path string, want int) map[string]any {
		t.Helper()
		code, out := f.request("GET", path, nil, false)
		if code != want {
			t.Fatalf("GET %s=%d want %d: %v", path, code, want, out)
		}
		return out
	}
	source := localWechatMemorySource{Connection: worker.connectionID, Account: account, Thread: thread, Dataset: "archive"}
	query := url.Values{"app_id": {"wechat"}, "connection_id": {source.Connection}, "account_id": {account}, "thread_id": {thread}, "dataset": {"archive"}}
	local := get(palace, 200)
	if raw, _ := json.Marshal(local); strings.Contains(string(raw), "星港") {
		t.Fatal("default palace loaded archived source", string(raw))
	}
	listing := get(palace+"/sources", 200)
	raw, _ := json.Marshal(listing)
	if strings.Contains(string(raw), "星港") || !strings.Contains(string(raw), `"dataset":"archive"`) {
		t.Fatal("directory omitted archive or retained source text", string(raw))
	}
	liveQuery := url.Values{"app_id": {"wechat"}, "connection_id": {source.Connection}, "account_id": {account}, "thread_id": {thread}}
	livePage := get(palace+"?"+liveQuery.Encode(), 200)
	if len(livePage["items"].([]any)) != 5 || livePage["scope_version"] == "" {
		t.Fatal("live WeChat scope did not reach palace", livePage)
	}
	liveID := livePage["items"].([]any)[0].(map[string]any)["id"].(string)
	liveQuery.Set("scope_version", livePage["scope_version"].(string))
	if result := get(palace+"/"+liveID+"?"+liveQuery.Encode(), 200); result["detail"] == "" {
		t.Fatal("live WeChat detail absent", result)
	}
	page := get(palace+"?"+query.Encode(), 200)
	if len(page["items"].([]any)) != 5 || page["next_cursor"] == "" || page["scope_version"] == "" {
		t.Fatal("archive page", page)
	}
	if raw, _ := json.Marshal(page); strings.Contains(string(raw), "封存派生正文末尾") || strings.Contains(string(raw), "历史原文页尾") {
		t.Fatal("summary list returned originals", string(raw))
	}
	record := page["items"].([]any)[0].(map[string]any)["id"].(string)
	detail := query
	get(palace+"/"+record+"?"+detail.Encode(), 409)
	detail.Set("scope_version", page["scope_version"].(string))
	full := get(palace+"/"+record+"?"+detail.Encode(), 200)
	if !strings.Contains(full["detail"].(string), "封存派生正文末尾") || strings.Contains(fmt.Sprint(full["sources"]), "历史原文页尾") {
		t.Fatal("derived detail crossed into source originals", full)
	}
	detail.Set("source_index", "0")
	detail.Set("length", "45")
	part := get(palace+"/"+record+"?"+detail.Encode(), 200)
	if part["original"] != true || part["has_more"] != true || strings.Contains(part["detail"].(string), "历史原文页尾") {
		t.Fatal("original source byte page", part)
	}
	detail.Set("offset", fmt.Sprint(int(part["next_offset"].(float64))))
	detail.Set("length", "4000")
	if out := get(palace+"/"+record+"?"+detail.Encode(), 200); !strings.Contains(out["detail"].(string), "历史原文页尾") {
		t.Fatal(out)
	}
	detail.Del("offset")
	detail.Del("length")
	detail.Del("source_index")
	detail.Set("cursor", page["next_cursor"].(string))
	next := get(palace+"?"+detail.Encode(), 200)
	if len(next["items"].([]any)) != 3 || next["next_cursor"] != "" {
		t.Fatal("archive continuation", next)
	}
	detail.Del("cursor")
	detail.Set("thread_id", denied)
	get(palace+"?"+detail.Encode(), 403)
	detail.Set("thread_id", thread)
	detail.Set("account_id", "another-account")
	get(palace+"?"+detail.Encode(), 503)
	detail.Set("account_id", account)
	call := func(tools *localToolSet, args map[string]any) (string, error) {
		encoded, _ := json.Marshal(args)
		return tools.Call(t.Context(), localWechatMemoryTool, encoded)
	}
	search := map[string]any{"action": "search", "connection_id": source.Connection, "account_id": account, "thread_id": thread, "dataset": "archive", "query": "星港"}
	tools := newTools("archive-search")
	result, err := call(tools, search)
	var hits struct {
		Items []struct{ Reference string }
		Next  string `json:"next_cursor"`
	}
	if err != nil || json.Unmarshal([]byte(result), &hits) != nil || len(hits.Items) != 5 || hits.Next == "" ||
		strings.Contains(result, "封存派生正文末尾") {
		t.Fatal(result, err)
	}
	ref := hits.Items[0].Reference
	if result, err := call(tools, map[string]any{"action": "read", "reference": ref, "source_index": 0}); err != nil || !strings.Contains(result, "历史原文页尾") {
		t.Fatal("tool original read", result, err)
	}
	if result, err := call(tools, map[string]any{"action": "search", "cursor": hits.Next, "limit": 2}); err != nil || !strings.Contains(result, `"effective_limit":2`) {
		t.Fatal("tool continuation", result, err)
	}
	if _, err := call(newTools("archive-reference-cross-turn"), map[string]any{"action": "read", "reference": ref}); err == nil {
		t.Fatal("archive reference escaped original request")
	}
	scope := companion.ScopeID("wechat", account, thread)
	originals, err := live.SearchMemory(t.Context(), companion.MemoryQuery{Scopes: []string{scope}, Query: "星港", Originals: true})
	if err != nil || len(originals.Items) != 0 {
		t.Fatal("archive query copied old records into live memory", originals, err)
	}
	calls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			calls++
			history := localModelHistory(turns)
			if calls == 1 {
				if strings.Contains(history, "星港旧摘要") || strings.Contains(history, "星港原始消息") {
					t.Fatal("first model input preloaded archived source")
				}
				raw, _ := json.Marshal(map[string]any{"action": "tool", "tool": localWechatMemoryTool, "arguments": search})
				return string(raw), nil
			}
			if calls == 2 {
				if !strings.Contains(history, "星港旧摘要") || strings.Contains(history, "历史原文页尾") {
					t.Fatal("archive search did not return summary-only evidence")
				}
				return `{"action":"reply","messages":["本轮封存档案查询结果"]}`, nil
			}
			if strings.Contains(history, "本轮封存档案查询结果") || strings.Contains(history, "星港旧摘要") {
				t.Fatal("next model request inherited archived evidence")
			}
			return `{"action":"reply","messages":["正常继续聊天"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, "archive-model", "查微信旧档案的星港记录"); code != 200 || out["status"] != "completed" {
		t.Fatal(code, out)
	}
	if code, out := localChatSend(f, "archive-model-next", "继续聊天"); code != 200 || out["status"] != "completed" {
		t.Fatal(code, out)
	}
	changed := bundle
	changed.Selection.Memories = append([]companion.LegacyMemoryFact{}, bundle.Selection.Memories...)
	changed.Selection.Memories[0].Text = "星港已替换的封存摘要"
	write(changed)
	get(palace+"/"+record+"?"+detail.Encode(), 409)
	if _, err := call(tools, map[string]any{"action": "read", "reference": ref}); err == nil {
		t.Fatal("archive replacement kept an old tool reference")
	}
	write(bundle)
	event := bundle.Selection.Events[0]
	changedText := "管理员更正了旧星港消息"
	input := companion.Input{Channel: "wechat", Account: account, Thread: thread, Sender: event.Sender, TraceID: event.MessageID, Text: changedText, OriginalText: &changedText,
		MessageTime: event.OccurredAt, Source: &companion.SourceRef{MessageID: event.MessageID, ServerID: event.ServerID, OccurredAt: event.OccurredAt, Revision: 2}}
	config := companion.Defaults()
	config.Enabled, config.Permissions.Remember = true, true
	if err := live.Observe(input, config, time.Now()); err != nil {
		t.Fatal(err)
	}
	get(palace+"/"+record+"?"+detail.Encode(), 409)
	detail.Del("scope_version")
	page = get(palace+"?"+detail.Encode(), 200)
	if raw, _ := json.Marshal(page); strings.Contains(string(raw), "星港旧摘要") || strings.Contains(string(raw), "星港原始消息0") {
		t.Fatal("archive bypassed corrected source", string(raw))
	}
	if err := live.Clear(scope); err != nil {
		t.Fatal(err)
	}
	get(palace+"?"+detail.Encode(), 403)
	get(palace+"/"+liveID+"?"+liveQuery.Encode(), 409)
}

func TestArchivedMemorySourceDirectoryResumesWithoutReadingOriginals(t *testing.T) {
	record := localWechatArchiveRecord{ID: "synthetic-record", Text: "合成档案", Version: 1}
	for i := range 25 {
		record.Sources = append(record.Sources, companion.LegacyMemoryEvent{MessageID: fmt.Sprint("synthetic-", i), Text: "私有原文"})
	}
	first, err := archiveMemoryDetail(record, nil, localMemoryReadInput{ID: record.ID})
	if err != nil || len(first["sources"].([]map[string]any)) != 20 || first["next_source_offset"] != 20 || first["source_has_more"] != true ||
		strings.Contains(fmt.Sprint(first["sources"]), "私有原文") {
		t.Fatal(first, err)
	}
	offset, limit := 20, 3
	next, err := archiveMemoryDetail(record, nil, localMemoryReadInput{ID: record.ID, SourceOffset: &offset, SourceLimit: &limit})
	if err != nil || len(next["sources"].([]map[string]any)) != 3 || next["next_source_offset"] != 23 || next["requested_source_limit"] != 3 {
		t.Fatal(next, err)
	}
	index := 24
	original, err := archiveMemoryDetail(record, &index, localMemoryReadInput{ID: record.ID})
	if err != nil || original["detail"] != "私有原文" || original["original"] != true {
		t.Fatal(original, err)
	}
}
