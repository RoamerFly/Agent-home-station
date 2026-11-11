package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/stationbrowser"
)

func TestStationDouyinMemoryCapturesOnlyAuthorizedSources(t *testing.T) {
	f, m, publicVideo, view := syntheticInteractionFixture(t, false)
	const observe = "/api/local/apps/observation"
	var workReads, commentReads, noticeReads int
	account, caption := "creator-synthetic", "合成银河说明"+strings.Repeat("甲", 260)+"页尾证据"
	m.readWorks = func(_ context.Context, _ string, _ stationbrowser.DouyinCreatorWorksRequest, authorize func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		workReads++
		works := make([]stationbrowser.DouyinCreatorWork, 6)
		for i := range works {
			works[i] = stationbrowser.DouyinCreatorWork{ID: fmt.Sprint(7300000000000000000 + int64(i)), Title: "银河作品",
				Caption: caption, Visibility: "公开", Video: true}
		}
		return stationbrowser.DouyinCreatorWorksPage{Account: account, PageKey: "synthetic-page", Works: works, CoverageKnown: true}, authorize()
	}
	m.readComments = func(_ context.Context, _ string, in stationbrowser.DouyinCommentsRequest, authorize func() error) (stationbrowser.DouyinCommentPage, error) {
		commentReads++
		return stationbrowser.DouyinCommentPage{Complete: true, Comments: []stationbrowser.DouyinComment{
			{ID: "23456", Author: "合成作者", AuthorID: "synthetic-author", Text: "合成评论 " + in.VideoID},
		}}, authorize()
	}
	noticeAccount := strings.Repeat("b", 64)
	m.probeNotice = func(_ context.Context, _ string, authorize func() error) (string, error) {
		return noticeAccount, authorize()
	}
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, authorize func() error) (stationbrowser.DouyinNoticePage, error) {
		noticeReads++
		if !in.AllowMarkRead || in.Account != noticeAccount {
			t.Fatal("notice read lost its authorization")
		}
		return stationbrowser.DouyinNoticePage{Account: noticeAccount, PagesRead: 1, RenderedText: "合成通知批次银河",
			Items: []stationbrowser.DouyinNotice{{ID: "76543", Type: 2, WorkID: "7300000000000000000"}}}, authorize()
	}
	update := func(action string, fields map[string]any, csrf bool, want int) {
		t.Helper()
		fields["action"], fields["revision"] = action, view["revision"]
		code, out := f.request("POST", localDouyinWebRoute, fields, csrf)
		if code != want {
			t.Fatalf("%s: %d %v", action, code, out)
		}
		if code == 200 {
			view = out
		}
	}
	grant := func(kind string, allowed bool, csrf bool, want int) {
		t.Helper()
		update("set_memory", map[string]any{"memory_scope": kind, "allow_memory": allowed}, csrf, want)
	}
	read := func(field, content string) (int, map[string]any) {
		t.Helper()
		return f.request("POST", observe, map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID,
			"content_id": content, "field": field, "refresh": true}, true)
	}
	newTools := func(id, original string) *localToolSet {
		t.Helper()
		store, in, err := f.handler.chat.conversation()
		if err != nil {
			t.Fatal(err)
		}
		in.TraceID, in.Text, in.OriginalText = id, original, &original
		in.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
		in.Source = &companion.SourceRef{MessageID: id}
		if err := store.Observe(in, localConversationConfig(""), time.Now()); err != nil {
			t.Fatal(err)
		}
		run, err := store.OpenLocalToolRun(t.Context(), in, true)
		if err != nil {
			t.Fatal(err)
		}
		tools := f.handler.chat.tools(store, in)
		tools.run = run
		return tools
	}
	call := func(tools *localToolSet, fields map[string]any) (map[string]any, error) {
		t.Helper()
		fields["app_id"] = "douyin"
		raw, _ := json.Marshal(fields)
		result, err := tools.Call(t.Context(), localWechatMemoryTool, raw)
		var out map[string]any
		if err == nil && json.Unmarshal([]byte(result), &out) != nil {
			t.Fatal(result)
		}
		return out, err
	}
	sources := func(tools *localToolSet) map[string]string {
		t.Helper()
		out, err := call(tools, map[string]any{"action": "sources"})
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]string{}
		for _, raw := range out["sources"].([]any) {
			item := raw.(map[string]any)
			found[item["kind"].(string)] = item["source_id"].(string)
		}
		return found
	}
	grant(douyinMemoryWorks, true, true, http.StatusConflict)
	update("set_works", map[string]any{"allow_works": true}, true, http.StatusOK)
	if code, out := read("account_works", douyinapp.WebWorksContentID); code != 200 {
		t.Fatal(code, out)
	}
	if len(sources(newTools("default-douyin", "查询抖音银河记忆"))) != 0 {
		t.Fatal("default scope exposed")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "apps", "douyin", "memory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained without opt-in: %v", err)
	}
	grant(douyinMemoryWorks, true, false, http.StatusForbidden)
	grant(douyinMemoryWorks, true, true, http.StatusOK)
	grant(douyinMemoryComments, true, true, http.StatusOK)
	code, bound := f.request("POST", localDouyinNoticePath, map[string]any{"action": "bind", "revision": view["revision"]}, true)
	if code != 200 {
		t.Fatal(code, bound)
	}
	view["revision"] = bound["revision"]
	grant(douyinMemoryNotices, true, true, http.StatusConflict)
	update("set_notice_agent", map[string]any{"allow_notice_agent": true}, true, http.StatusOK)
	grant(douyinMemoryNotices, true, true, http.StatusOK)
	tools := newTools("read-douyin-memory", "查询抖音银河记忆")
	available := sources(tools)
	if len(available) != 3 || workReads != 1 || commentReads != 0 || noticeReads != 0 {
		t.Fatal("source discovery touched upstream", available, workReads, commentReads, noticeReads)
	}
	if _, err := call(newTools("implicit-douyin", "查询银河记忆"), map[string]any{"action": "search", "source_id": available[douyinMemoryWorks], "query": "银河"}); !errors.Is(err, apphost.ErrObservationForbidden) {
		t.Fatal("implicit external source request passed", err)
	}
	code, works := read("account_works", douyinapp.WebWorksContentID)
	if code != 200 {
		t.Fatal(code, works)
	}
	creatorVideo := works["chunks"].([]any)[0].(map[string]any)["follow_up_content_id"].(string)
	if code, out := read("comments", creatorVideo); code != 200 {
		t.Fatal(code, out)
	}
	// A public URL observed outside the creator list cannot enter owner memory.
	publicVideo = syntheticDouyinWebPlaybackTarget(t, f, m)
	if code, out := read("comments", publicVideo); code != 200 {
		t.Fatal(code, out)
	}
	code, ticket := f.request("POST", localDouyinNoticePath, map[string]any{"action": "prepare", "revision": view["revision"], "allow_mark_read": true}, true)
	if code != 200 {
		t.Fatal(code, ticket)
	}
	if code, out := f.request("POST", localDouyinNoticePath, map[string]any{"action": "read", "revision": view["revision"], "allow_mark_read": true, "ticket": ticket["ticket"]}, true); code != 200 {
		t.Fatal(code, out)
	}
	search := func(tools *localToolSet, kind, query string) map[string]any {
		t.Helper()
		out, err := call(tools, map[string]any{"action": "search", "source_id": available[kind], "query": query})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	page := search(tools, douyinMemoryWorks, "银河")
	if page["effective_limit"] != float64(5) || len(page["items"].([]any)) != 5 || page["next_cursor"] == "" {
		t.Fatal("pagination absent", page)
	}
	item := page["items"].([]any)[0].(map[string]any)
	ref := item["reference"]
	if strings.Contains(item["summary"].(string), "页尾证据") {
		t.Fatal("search returned full original")
	}
	body, err := call(tools, map[string]any{"action": "read", "reference": ref, "length": 48})
	if err != nil || body["has_more"] != true || body["effective_length"].(float64) > 48 {
		t.Fatal("original page", body, err)
	}
	body, err = call(tools, map[string]any{"action": "read", "reference": ref, "offset": body["next_offset"]})
	if err != nil || !strings.Contains(body["text"].(string), "页尾证据") {
		t.Fatal("original continuation", body, err)
	}
	next, err := call(tools, map[string]any{"action": "search", "cursor": page["next_cursor"], "limit": 1})
	if err != nil || len(next["items"].([]any)) != 1 || next["next_cursor"] != "" {
		t.Fatal("search continuation", next, err)
	}
	if _, err := call(newTools("foreign-reference", "查询抖音银河记忆"), map[string]any{"action": "read", "reference": ref}); !errors.Is(err, apphost.ErrObservationForbidden) {
		t.Fatal("reference escaped original request", err)
	}
	if _, err := tools.Call(t.Context(), localInteractionToolName, json.RawMessage(`{}`)); !errors.Is(err, apphost.ErrObservationForbidden) {
		t.Fatal("memory made available to a side-effecting tool", err)
	}
	if items := search(tools, douyinMemoryComments, "合成评论")["items"].([]any); len(items) != 1 || items[0].(map[string]any)["work_id"] != "7300000000000000000" {
		t.Fatal("comments crossed creator scope", items)
	}
	if items := search(tools, douyinMemoryNotices, "银河")["items"].([]any); len(items) != 1 || items[0].(map[string]any)["work_id"] != "" {
		t.Fatal("rendered notification text falsely attributed to an item", items)
	}
	if workReads != 2 || commentReads != 2 || noticeReads != 1 {
		t.Fatal("retrieval read platform", workReads, commentReads, noticeReads)
	}
	t.Run("model starts without app body and next turn omits response", func(t *testing.T) {
		calls := 0
		f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
			return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
				calls++
				history := localModelHistory(turns)
				switch calls {
				case 1:
					if strings.Contains(history, "合成银河说明") {
						t.Fatal("first model input loaded other app memory")
					}
					return localToolCall(localWechatMemoryTool, map[string]any{"action": "search", "app_id": "douyin",
						"source_id": available[douyinMemoryWorks], "query": "银河", "limit": 1}), nil
				case 2:
					if !strings.Contains(history, "合成银河说明") || strings.Contains(history, "页尾证据") {
						t.Fatal("summary did not reach model within this request")
					}
					return `{"action":"reply","messages":["本轮私用的银河结果"]}`, nil
				default:
					if strings.Contains(history, "本轮私用的银河结果") || strings.Contains(history, "合成银河说明") {
						t.Fatal("another request inherited prior results")
					}
					return `{"action":"reply","messages":["下一轮"]}`, nil
				}
			}), nil
		}
		if code, out := localChatSend(f, "douyin-memory-model", "请查询抖音银河记忆"); code != 200 || out["status"] != "completed" {
			t.Fatal(code, out)
		}
		if code, out := localChatSend(f, "douyin-memory-next-model", "继续聊天"); code != 200 || out["status"] != "completed" {
			t.Fatal(code, out)
		}
		if calls != 3 {
			t.Fatal(calls)
		}
	})
	caption = "银河作品有新的平台说明"
	if code, out := read("account_works", douyinapp.WebWorksContentID); code != 200 {
		t.Fatal(code, out)
	}
	if !errors.Is(tools.checkAppObservations(t.Context()), companion.ErrMemoryConflict) {
		t.Fatal("corrected evidence remained usable")
	}
	fresh := newTools("switch-douyin-account", "查询抖音银河记忆")
	search(fresh, douyinMemoryWorks, "银河")
	account = "different-platform-account"
	_, _ = read("account_works", douyinapp.WebWorksContentID)
	if fresh.checkAppObservations(t.Context()) == nil {
		t.Fatal("account change left previous evidence usable")
	}
	afterChange := sources(newTools("changed-douyin-account", "查询抖音银河记忆"))
	if len(afterChange) != 1 || afterChange[douyinMemoryNotices] == "" {
		t.Fatal("creator change crossed notice account or left creator grants readable", afterChange)
	}
	noticeOnly := newTools("check-notice-revoke", "查询抖音银河记忆")
	search(noticeOnly, douyinMemoryNotices, "银河")
	grant(douyinMemoryNotices, false, true, http.StatusOK)
	if noticeOnly.checkAppObservations(t.Context()) == nil {
		t.Fatal("revoked notice source remained usable for the final reply")
	}
	grant(douyinMemoryNotices, true, true, http.StatusOK)
	afterGrant := newTools("regrant-douyin-notices", "查询抖音银河记忆")
	latest := sources(afterGrant)
	if latest[douyinMemoryNotices] == available[douyinMemoryNotices] {
		t.Fatal("revoked source ID reused")
	}
	available = latest
	if items := search(afterGrant, douyinMemoryNotices, "银河")["items"].([]any); len(items) != 0 {
		t.Fatal("new authorization revived revoked records", items)
	}
	db, err := openLocalDouyinMemory(t.Context(), m.home, false)
	if err != nil {
		t.Fatal(err)
	}
	var searches, reads, items, tokens int
	if err := db.QueryRow("SELECT SUM(searches),SUM(reads),SUM(items),SUM(estimated_tokens) FROM audit").Scan(&searches, &reads, &items, &tokens); err != nil ||
		searches < 5 || reads != 2 || items < 10 || tokens < 1 {
		t.Fatal("incomplete audit", searches, reads, items, tokens, err)
	}
	db.Close()
	code, ticket = f.request("POST", localDouyinNoticePath, map[string]any{"action": "prepare", "revision": view["revision"], "allow_mark_read": true}, true)
	if code != 200 {
		t.Fatal(code, ticket)
	}
	if code, out := f.request("POST", localDouyinNoticePath, map[string]any{"action": "read", "revision": view["revision"], "allow_mark_read": true, "ticket": ticket["ticket"]}, true); code != 200 {
		t.Fatal(code, out)
	}
	registry := f.handler.chat.applications
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLocalHomeHandlerWithApps(f.dir, f.root, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	f = newLocalAppObservationFixtureWithHandler(t, restarted, f.dir, f.root)
	restartedTools := newTools("restart-douyin-memory", "查询抖音银河记忆")
	if items := search(restartedTools, douyinMemoryNotices, "银河")["items"].([]any); len(items) != 1 {
		t.Fatal("authorized memory did not survive restart", items)
	}
}
