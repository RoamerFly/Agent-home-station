package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func checkStationMemoryDirection(t *testing.T, f *localHomeFixture, base, origin, source, ownerID string, makeTools func(string, string, string, ...string) (*stationAttachmentTools, companion.Input), config companion.Config) {
	t.Helper()
	host := f.handler.chat.wechat
	store, local, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	seed := func(i int, thread, text string) companion.MemoryEntry {
		in := local
		in.Thread = thread
		in.TraceID = fmt.Sprintf("station-memory-%d", i)
		in.Text = text
		in.OriginalText = &text
		in.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
		in.Source = &companion.SourceRef{MessageID: in.TraceID, OccurredAt: in.MessageTime}
		if err := store.Observe(in, localConversationConfig(""), time.Now()); err != nil {
			t.Fatal(err)
		}
		ref, ok := store.SourceForInput(in)
		if !ok {
			t.Fatal("local source unavailable")
		}
		entry, err := store.SaveMemory(t.Context(), companion.MemoryEntry{Scope: companion.ScopeID(in.Channel, in.Account, in.Thread), Subject: in.Sender, Kind: "fact", Manual: true, Summary: "工作站专属摘要 " + text, Detail: "工作站私有原话 " + text, Sources: []companion.MemorySource{{EventID: ref.EventID, Quote: text}}}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	var old companion.MemoryEntry
	for i := range 6 {
		old = seed(i, local.Thread, fmt.Sprintf("工作站专属测试计划%d", i))
	}
	seed(7, "synthetic-other-local-user", "工作站专属测试计划 hidden-scope")
	route := base + "/station-memory-grants"
	req := func(method, path string, body any, csrf bool, want int) map[string]any {
		t.Helper()
		code, out := f.request(method, path, body, csrf)
		if code != want {
			t.Fatalf("%s %s = %d want %d: %+v", method, path, code, want, out)
		}
		return out
	}
	const question = "请查工作站聊天里记录的测试计划"
	query := json.RawMessage(`{"action":"search","source":"station","query":"专属测试计划"}`)
	call := func(tools *stationAttachmentTools, args json.RawMessage) (string, error) {
		return tools.Call(t.Context(), stationWechatCrossToolName, args)
	}
	deny := func(tools *stationAttachmentTools, args json.RawMessage) {
		t.Helper()
		if raw, err := call(tools, args); err == nil {
			t.Fatal("unauthorized result", raw)
		}
	}
	before := req("GET", route, nil, false, 200)
	if before["revision"] != float64(0) || before["scopes_current"] != false {
		t.Fatal(before)
	}
	disabled, _ := makeTools("workstation-disabled", origin, ownerID, question)
	deny(disabled, query)
	grant := map[string]any{"revision": 0, "owner_revision": 1, "request_key": "enable-station-query", "origins": []string{origin, ownerID}}
	req("PUT", route, grant, false, 403)
	grant["sources"] = []string{source}
	req("PUT", route, grant, true, 400)
	delete(grant, "sources")
	req("PUT", route, grant, true, 200)
	req("PUT", route, grant, true, 200)
	if req("GET", base+"/cross-memory-grants", nil, false, 200)["revision"] != float64(3) {
		t.Fatal("workstation direction overwrote same-account cross grants")
	}
	if out := req("GET", base+"/memory-grants", nil, false, 200); len(out["station_reads"].(map[string]any)) != 0 {
		t.Fatal("workstation direction changed the opposite grant", out)
	}
	ordinary, _ := makeTools("workstation-ordinary", origin, ownerID, "聊聊今天的天气")
	deny(ordinary, query)
	member, _ := makeTools("workstation-member", origin, "wxid-other", question)
	deny(member, query)
	group, in := makeTools("workstation-group", origin, ownerID, question)
	foreign := group.ForkTools().(*stationAttachmentTools)
	in.Account = "two"
	foreign.SetTrace(in)
	deny(foreign, query)
	spoofed := group.ForkTools().(*stationAttachmentTools)
	spoofed.SetTrace(group.trace)
	deny(spoofed, json.RawMessage(`{"action":"search","source":"station","thread_id":"synthetic-other-local-user","query":"专属测试计划"}`))
	stationReads := 0
	conversation := host.stationConversation
	host.stationConversation = func() (*companion.Store, companion.Input, error) { stationReads++; return conversation() }
	defer func() { host.stationConversation = conversation }()
	if tools, err := group.List(t.Context()); err != nil || len(tools) != 1 || tools[0].Name != stationWechatCrossToolName || stationReads != 0 || group.cross != nil {
		t.Fatal("discovery loaded workstation content", tools, stationReads, err)
	}
	raw, err := call(group, query)
	if err != nil || strings.Contains(raw, "hidden-scope") || strings.Contains(raw, "工作站私有原话") {
		t.Fatal(raw, err)
	}
	var page struct {
		Items  []struct{ Reference string }
		Next   string `json:"next_cursor"`
		Source string
	}
	if json.Unmarshal([]byte(raw), &page) != nil || len(page.Items) != 5 || page.Next == "" || page.Source != "station" {
		t.Fatal(raw)
	}
	read, _ := json.Marshal(map[string]any{"action": "read", "reference": page.Items[0].Reference})
	if raw, err := call(group, read); err != nil || !strings.Contains(raw, "工作站私有原话") || strings.Contains(raw, "hidden-scope") {
		t.Fatal(raw, err)
	}
	deny(ordinary, read)
	next, _ := json.Marshal(map[string]any{"action": "search", "cursor": page.Next, "limit": 1})
	if raw, err := call(group, next); err != nil || strings.Contains(raw, "hidden-scope") {
		t.Fatal(raw, err)
	}
	old.Summary = "工作站已经更正的测试计划"
	if _, err := store.SaveMemory(t.Context(), old, old.Version); err != nil {
		t.Fatal(err)
	}
	deny(group, read)
	if err := group.RoutePrivateReply(t.Context(), []string{"不应提交的旧工作站证据"}); err == nil {
		t.Fatal("edited workstation evidence was deliverable")
	}
	for _, thread := range []string{origin, ownerID} {
		tools, input := makeTools("workstation-engine-"+thread, thread, ownerID, question)
		calls := 0
		model := localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			calls++
			raw, _ := json.Marshal(turns)
			if calls == 1 {
				if strings.Contains(string(raw), "工作站专属摘要") || strings.Contains(string(raw), "工作站私有原话") {
					return "", errors.New("workstation data loaded before query")
				}
				return `{"action":"tool","tool":"wechat_cross_memory","arguments":{"action":"search","source":"station","query":"专属测试计划"}}`, nil
			}
			if !strings.Contains(string(raw), "工作站专属摘要") || strings.Contains(string(raw), "hidden-scope") || strings.Contains(string(raw), "工作站私有原话") {
				return "", errors.New("unexpected query scope")
			}
			return `{"action":"reply","messages":["合成工作站查询结果"]}`, nil
		})
		engine, err := companion.New(config, tools.store, model, "合成工作站查询回放")
		if err != nil {
			t.Fatal(err)
		}
		engine.Tools, engine.MaxToolCalls = tools, 6
		decision, err := engine.Respond(t.Context(), input)
		if err != nil || decision.Action != "silent" || decision.Reason != "cross_conversation_result_private_delivery_handled" || calls != 2 {
			t.Fatal(decision, calls, err)
		}
		for _, turn := range tools.store.Context(input, config, time.Now()).Messages {
			if strings.Contains(turn.Text, "合成工作站查询结果") || strings.Contains(turn.Text, "工作站专属摘要") {
				t.Fatal("workstation answer entered ordinary memory", turn)
			}
		}
	}
	changed, _ := makeTools("workstation-grant-change", origin, ownerID, question)
	if _, err := call(changed, query); err != nil {
		t.Fatal(err)
	}
	grant["revision"], grant["request_key"], grant["origins"] = 1, "disable-station-query", []string{}
	req("PUT", route, grant, true, 200)
	deny(changed, query)
	if err := changed.RoutePrivateReply(t.Context(), []string{"不应提交的旧工作站证据"}); err == nil {
		t.Fatal("revoked workstation result remained deliverable")
	}
	wechat, _ := makeTools("workstation-wechat-still-allowed", origin, ownerID, "请查别的群聊的旅行资料")
	if raw, err := call(wechat, json.RawMessage(fmt.Sprintf(`{"action":"search","thread_id":%q,"query":"旅行"}`, source))); err != nil || !strings.Contains(raw, "合成旅行摘要") {
		t.Fatal(raw, err)
	}
}
