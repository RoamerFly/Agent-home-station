package station

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/stationbrowser"
)

func checkWechatDouyinMemoryDirection(t *testing.T, f *localHomeFixture, base, origin, ownerID string, makeTools func(string, string, string, ...string) (*stationAttachmentTools, companion.Input), config companion.Config) {
	t.Helper()
	const question = "请查抖音里留存的银河作品资料"
	host := f.handler.chat.wechat
	manager := f.handler.home.douyinWeb
	manager.available = func() bool { return true }
	manager.open = func(context.Context, string) (localDouyinLoginWindow, error) {
		window := new(syntheticDouyinLoginWindow)
		window.active.Store(true)
		return window, nil
	}
	req := func(method, route string, body any, csrf bool, want int) map[string]any {
		t.Helper()
		code, out := f.request(method, route, body, csrf)
		if code != want {
			t.Fatalf("%s %s = %d want %d: %+v", method, route, code, want, out)
		}
		return out
	}
	route := base + "/douyin-memory-grants"
	if out := req("GET", route, nil, false, 200); out["revision"] != float64(0) || out["scopes_current"] != false {
		t.Fatal("direction enabled by default", out)
	}
	_, view := f.request("GET", localDouyinWebRoute, nil, false)
	view = req("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": view["revision"], "approve_profile": true}, true, 200)
	view = req("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true, 200)
	view = req("POST", localDouyinWebRoute, map[string]any{"action": "set_works", "revision": view["revision"], "allow_works": true}, true, 200)
	view = req("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "revision": view["revision"], "memory_scope": "works", "allow_memory": true}, true, 200)
	reads, caption := 0, "微信抖音专属原文"+strings.Repeat("甲", 240)+"银河末尾证据"
	manager.readWorks = func(_ context.Context, _ string, _ stationbrowser.DouyinCreatorWorksRequest, authorize func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		reads++
		works := make([]stationbrowser.DouyinCreatorWork, 6)
		for i := range works {
			works[i] = stationbrowser.DouyinCreatorWork{ID: fmt.Sprint(7300000000000000000 + int64(i)), Title: "合成银河作品", Caption: caption, Visibility: "仅自己可见"}
		}
		return stationbrowser.DouyinCreatorWorksPage{Account: "synthetic-wechat-douyin", PageKey: "synthetic-page", Works: works, CoverageKnown: true}, authorize()
	}
	observe := func() {
		t.Helper()
		req("POST", "/api/local/apps/observation", map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID,
			"content_id": douyinapp.WebWorksContentID, "field": "account_works", "refresh": true}, true, 200)
	}
	observe()
	call := func(tools *stationAttachmentTools, fields map[string]any) (string, error) {
		raw, _ := json.Marshal(fields)
		return tools.Call(t.Context(), stationWechatCrossToolName, raw)
	}
	query := map[string]any{"action": "search", "source": "douyin", "query": "银河"}
	disabled, _ := makeTools("douyin-disabled", origin, ownerID, question)
	if result, err := call(disabled, query); err == nil {
		t.Fatal("workstation permission enabled WeChat direction", result)
	}
	enable := map[string]any{"revision": 0, "owner_revision": 1, "request_key": "allow-douyin-memory", "origins": []string{origin, ownerID}, "app_sources": []string{"works"}}
	req("PUT", route, enable, false, 403)
	enable["app_sources"] = []string{"notices"}
	req("PUT", route, enable, true, 409)
	enable["app_sources"] = []string{"works"}
	req("PUT", route, enable, true, 200)
	req("PUT", route, enable, true, 200)
	group, input := makeTools("douyin-source-group", origin, ownerID, question)
	readCount := reads
	if _, err := group.List(t.Context()); err != nil || reads != readCount || group.cross != nil {
		t.Fatal("discovery loaded source content", err)
	}
	raw, err := call(group, map[string]any{"action": "sources"})
	var directory struct {
		Sources []string
		Douyin  []localDouyinMemorySource `json:"douyin_sources"`
	}
	if err != nil || json.Unmarshal([]byte(raw), &directory) != nil || len(directory.Douyin) != 1 || directory.Douyin[0].Kind != "works" {
		t.Fatal("source directory", raw, err)
	}
	query["source_id"] = directory.Douyin[0].Scope
	member, _ := makeTools("douyin-member", origin, "wxid-member", question)
	ordinary, _ := makeTools("douyin-ordinary", origin, ownerID, "查今天的天气")
	for _, denied := range []*stationAttachmentTools{member, ordinary, disabled} {
		if result, err := call(denied, query); err == nil {
			t.Fatal("unauthorized request read app source", result)
		}
	}
	raw, err = call(group, query)
	var page struct {
		Items []struct{ Reference string }
		Next  string `json:"next_cursor"`
		Limit int    `json:"effective_limit"`
	}
	if err != nil || json.Unmarshal([]byte(raw), &page) != nil || len(page.Items) != 5 || page.Next == "" || page.Limit != 5 ||
		strings.Contains(raw, "银河末尾证据") || !group.PrivateReplyRequired() || group.PrivateReplyTarget() != ownerID {
		t.Fatal("summary scope or route", raw, err)
	}
	ref := page.Items[0].Reference
	if len(group.cross.Used) != 1 || !group.cross.Used[0].Douyin || group.cross.Used[0].DouyinScope != directory.Douyin[0].Scope {
		t.Fatal("Douyin result did not bind the concrete scope")
	}
	if raw, err := call(group, map[string]any{"action": "read", "reference": ref}); err != nil || !strings.Contains(raw, "银河末尾证据") {
		t.Fatal("original read", raw, err)
	}
	if raw, err := call(group, map[string]any{"action": "search", "cursor": page.Next, "limit": 1}); err != nil || !strings.Contains(raw, `"effective_limit":1`) {
		t.Fatal("pagination", raw, err)
	}
	var restoredProofs []stationWechatCrossProof
	serialized, err := json.Marshal(group.cross.Proofs)
	if err != nil || json.Unmarshal(serialized, &restoredProofs) != nil {
		t.Fatal("durable source proof", err)
	}
	restored := group.ForkTools().(*stationAttachmentTools)
	restored.SetTrace(input)
	restored.cross = &stationWechatCrossTurn{Required: true, Target: ownerID, Proofs: restoredProofs}
	if err := restored.checkCrossResults(t.Context()); err != nil {
		t.Fatal("durable source proofs did not revalidate", err)
	}
	nextTurn := group.ForkTools().(*stationAttachmentTools)
	nextTurn.SetTrace(input)
	if raw, err := call(nextTurn, map[string]any{"action": "read", "reference": ref}); err == nil {
		t.Fatal("reference escaped original tool turn", raw)
	}
	for _, thread := range []string{origin, ownerID} {
		tools, in := makeTools("douyin-engine-"+thread, thread, ownerID, question)
		calls := 0
		model := localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			calls++
			history := localModelHistory(turns)
			if calls == 1 {
				if strings.Contains(history, "微信抖音专属原文") {
					t.Fatal("initial model input loaded external memory")
				}
				raw, _ := json.Marshal(map[string]any{"action": "tool", "tool": stationWechatCrossToolName, "arguments": query})
				return string(raw), nil
			}
			if !strings.Contains(history, "合成银河作品") || strings.Contains(history, "银河末尾证据") {
				t.Fatal("model summary scope", history)
			}
			return `{"action":"reply","messages":["合成抖音私下查询结果"]}`, nil
		})
		engine, err := companion.New(config, tools.store, model, "抖音按需查询回放")
		if err != nil {
			t.Fatal(err)
		}
		engine.Tools, engine.MaxToolCalls = tools, 6
		decision, err := engine.Respond(t.Context(), in)
		if err != nil || calls != 2 || decision.Action != "silent" || decision.Reason != "cross_conversation_result_private_delivery_handled" {
			t.Fatal(decision, calls, err)
		}
		for _, turn := range tools.store.Context(in, config, time.Now()).Messages {
			if strings.Contains(turn.Text, "合成抖音私下查询结果") || strings.Contains(turn.Text, "微信抖音专属原文") {
				t.Fatal("cross-app result entered ordinary context", turn)
			}
		}
	}
	if reads != readCount {
		t.Fatal("memory query contacted Douyin", reads, readCount)
	}
	caption = "银河作品已更正"
	observe()
	if group.checkCrossResults(t.Context()) == nil || group.RoutePrivateReply(t.Context(), []string{"应失效的旧证据"}) == nil {
		t.Fatal("updated source left old result deliverable")
	}
	if restored.checkCrossResults(t.Context()) == nil || restored.RoutePrivateReply(t.Context(), []string{"重启后的旧资料"}) == nil {
		t.Fatal("durable source proofs did not reject updated evidence")
	}
	fresh, _ := makeTools("douyin-before-direction-revoke", origin, ownerID, question)
	if _, err := call(fresh, query); err != nil {
		t.Fatal(err)
	}
	enable["revision"], enable["request_key"], enable["origins"], enable["app_sources"] = 1, "revoke-douyin-direction", []string{}, []string{}
	req("PUT", route, enable, true, 200)
	if fresh.checkCrossResults(t.Context()) == nil || fresh.RoutePrivateReply(t.Context(), []string{"已撤权的旧结果"}) == nil {
		t.Fatal("revoked direction left result deliverable")
	}
	owner, err := host.ownerReader()
	if err != nil {
		t.Fatal(err)
	}
	available, err := host.douyinDirectionSources(t.Context(), owner)
	if err != nil || len(available) != 1 {
		t.Fatal("WeChat revoke changed Station source", available, err)
	}
	enable["revision"], enable["request_key"], enable["origins"], enable["app_sources"] = 2, "reopen-douyin-direction", []string{origin}, []string{"works"}
	req("PUT", route, enable, true, 200)
	last, _ := makeTools("douyin-before-source-revoke", origin, ownerID, question)
	if _, err := call(last, query); err != nil {
		t.Fatal(err)
	}
	req("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "revision": view["revision"], "memory_scope": "works", "allow_memory": false}, true, 200)
	if last.checkCrossResults(t.Context()) == nil || last.RoutePrivateReply(t.Context(), []string{"来源已撤权"}) == nil {
		t.Fatal("source revoke left old result deliverable")
	}
	if out := req("GET", route, nil, false, 200); out["scopes_current"] != false {
		t.Fatal("stale source permission displayed as current", out)
	}
}
