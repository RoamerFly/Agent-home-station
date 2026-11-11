package station

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

func syntheticNoticeFixture(t *testing.T, enabled bool, extra ...apphost.Registration) (*localHomeFixture, *localDouyinWebLogin, map[string]any) {
	t.Helper()
	f, m := newLocalDouyinWebFixture(t, extra...)
	m.openCreatorWorks = m.openCreator
	initializeDouyinWeb(t, f)
	_, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	m.probeNotice = func(_ context.Context, _ string, check func() error) (string, error) {
		return strings.Repeat("a", 64), check()
	}
	code, view := f.request("POST", localDouyinNoticePath, map[string]any{"action": "bind", "revision": view["revision"]}, true)
	if code != 200 || view["notice_agent_enabled"] != false {
		t.Fatalf("binding enabled tool: %d %v", code, view)
	}
	if enabled {
		view = setSyntheticNoticeAgent(t, f, true)
	}
	return f, m, view
}

func setSyntheticNoticeAgent(t *testing.T, f *localHomeFixture, enabled bool) map[string]any {
	t.Helper()
	_, view := f.request("GET", localDouyinWebRoute, nil, false)
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_notice_agent", "revision": view["revision"], "allow_notice_agent": enabled}, true)
	if code != 200 || view["notice_agent_enabled"] != enabled {
		t.Fatalf("notice tool grant: %d %v", code, view)
	}
	return view
}

func noticeToolSet(t *testing.T, f *localHomeFixture, trace string, fresh bool) *localToolSet {
	t.Helper()
	store, in, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	text := "读取互动通知，允许标记已读"
	in.TraceID, in.Text, in.OriginalText = trace, text, &text
	in.Source = &companion.SourceRef{MessageID: trace}
	in.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
	if fresh {
		if err := store.Observe(in, localConversationConfig(""), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	run, err := store.OpenLocalToolRun(t.Context(), in, fresh)
	if err != nil {
		t.Fatal(err)
	}
	tools := f.handler.chat.tools(store, in)
	tools.run = run
	tools.inspectOnly = !run.Tracked || !fresh && len(run.Operations) > 0
	return tools
}

func inboxArgs(extra map[string]any) json.RawMessage {
	args := map[string]any{"action": "read", "app_id": "douyin", "allow_mark_read": true}
	for key, value := range extra {
		args[key] = value
	}
	raw, _ := json.Marshal(args)
	return raw
}

func TestLocalInboxGrantIsSeparateAndRevocableWhileOffline(t *testing.T) {
	f, m, view := syntheticNoticeFixture(t, false)
	reads := 0
	m.readNotice = func(context.Context, string, stationbrowser.DouyinNoticeRequest, func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		return stationbrowser.DouyinNoticePage{}, errors.New("must not run")
	}
	tools := noticeToolSet(t, f, "notice-grant", true)
	if value, err := tools.Call(t.Context(), localInboxToolName, inboxArgs(nil)); err == nil || value != "" || reads != 0 {
		t.Fatalf("binding became tool grant: %q %v", value, err)
	}
	if code, _ := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_notice_agent", "revision": view["revision"], "allow_notice_agent": true}, false); code != 403 {
		t.Fatalf("grant bypassed CSRF: %d", code)
	}
	setSyntheticNoticeAgent(t, f, true)
	value, err := tools.Call(t.Context(), localInboxToolName, json.RawMessage(`{"action":"status","app_id":"douyin"}`))
	if err != nil || !strings.Contains(value, `"available":true`) || strings.Contains(value, strings.Repeat("a", 64)) || reads != 0 {
		t.Fatalf("status touched platform or exposed account: %q %v", value, err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"action":"read","app_id":"douyin"}`),
		inboxArgs(map[string]any{"pages": 6}),
		inboxArgs(map[string]any{"pages": -1}),
		inboxArgs(map[string]any{"app_id": "wechat"}),
		inboxArgs(map[string]any{"action": "set_notice_agent"}),
		json.RawMessage(`{"action":"status","app_id":"douyin","allow_mark_read":false}`),
	} {
		if value, err := tools.Call(t.Context(), localInboxToolName, raw); err == nil || value != "" || reads != 0 {
			t.Fatalf("invalid request reached platform: %s %q %v", raw, value, err)
		}
	}
	_, view = f.request("GET", localDouyinWebRoute, nil, false)
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "pause", "revision": view["revision"]}, true)
	if code != 200 {
		t.Fatal(code, view)
	}
	m.available = func() bool { return false }
	if value, err := tools.Call(t.Context(), localInboxToolName, inboxArgs(nil)); err == nil || value != "" || reads != 0 {
		t.Fatalf("offline read: %q %v", value, err)
	}
	setSyntheticNoticeAgent(t, f, false)
}

func TestLocalInboxNormalChatReadsContinuesAndDoesNotPreload(t *testing.T) {
	f, m, _ := syntheticNoticeFixture(t, true)
	reads, calls := 0, 0
	checkpoint := stationbrowser.DouyinNoticeCheckpoint{Pages: 1, Digest: [32]byte{7}}
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		if in.Account != strings.Repeat("a", 64) || !in.AllowMarkRead || reads == 1 && in.Pages != 2 || reads == 2 && in.After != checkpoint {
			t.Errorf("lost scope or parameters: %+v", in)
		}
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		text := "合成通知正文一"
		if reads == 2 {
			text = "合成通知正文二"
		}
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, HasMore: reads == 1, Next: checkpoint, RenderedText: text, Items: []stationbrowser.DouyinNotice{{ID: "1234", Type: 2}}}, nil
	}
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
			calls++
			history := localModelHistory(turns)
			switch calls {
			case 1:
				if strings.Contains(history, "合成通知正文") || reads != 0 {
					t.Fatal("notifications preloaded")
				}
				return localToolCall("discover_tools", map[string]any{"query": localInboxToolName}), nil
			case 2:
				if !hasTool(tools, localInboxToolName) {
					t.Fatal("inbox tool not discoverable")
				}
				return localToolCall(localInboxToolName, map[string]any{"action": "status", "app_id": "douyin"}), nil
			case 3:
				if reads != 0 || !strings.Contains(history, `available`) {
					t.Fatal("status read platform")
				}
				return localToolCall(localInboxToolName, map[string]any{"action": "read", "app_id": "douyin", "allow_mark_read": true, "pages": 2}), nil
			case 4:
				if !strings.Contains(history, "合成通知正文一") {
					t.Fatal("tool text absent")
				}
				m.mu.Lock()
				var cursor string
				for key := range m.noticeCursors {
					cursor = key
				}
				m.mu.Unlock()
				if cursor == "" || !strings.Contains(history, cursor) {
					t.Fatal("continuation absent")
				}
				return localToolCall(localInboxToolName, map[string]any{"action": "read", "app_id": "douyin", "allow_mark_read": true, "continuation": cursor}), nil
			case 5:
				if !strings.Contains(history, "合成通知正文二") {
					t.Fatal("continued text absent")
				}
				return `{"action":"reply","messages":["已按授权读取通知，未执行回复。"]}`, nil
			default:
				if strings.Contains(history, "合成通知正文") {
					t.Fatal("raw tool text carried into ordinary next turn")
				}
				return `{"action":"reply","messages":["好的。"]}`, nil
			}
		}}, nil
	}
	text := "读取互动通知，允许标记已读"
	if code, out := localChatSend(f, "notice-normal", text); code != 200 || out["status"] != "completed" || reads != 2 {
		t.Fatalf("chat: %d %v reads=%d", code, out, reads)
	}
	if code, _ := localChatSend(f, "notice-normal", text); code != 200 || reads != 2 || calls != 5 {
		t.Fatal("chat replay repeated notifications")
	}
	tools := noticeToolSet(t, f, "notice-normal", false)
	if len(tools.run.Operations) != 2 {
		t.Fatalf("missing durable reservations: %+v", tools.run.Operations)
	}
	for _, op := range tools.run.Operations {
		if strings.Contains(op.Result, "合成通知") || op.Target != "douyin:notifications" {
			t.Fatalf("text entered checkpoint: %+v", op)
		}
	}
	value, err := tools.Call(t.Context(), localInboxToolName, inboxArgs(map[string]any{"pages": 2}))
	if err != nil || !strings.Contains(value, "read_not_repeated") || reads != 2 {
		t.Fatalf("recovery repeated read: %q %v", value, err)
	}
	if code, _ := localChatSend(f, "notice-next-normal", "你好"); code != 200 || reads != 2 {
		t.Fatal("ordinary next turn read inbox")
	}
}

func TestLocalInboxUnknownReadIsNotRepeatedAfterToolRebuild(t *testing.T) {
	f, m, _ := syntheticNoticeFixture(t, true)
	reads := 0
	m.readNotice = func(context.Context, string, stationbrowser.DouyinNoticeRequest, func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		return stationbrowser.DouyinNoticePage{}, errors.New("synthetic-secret-cookie")
	}
	tools := noticeToolSet(t, f, "notice-unknown", true)
	value, err := tools.Call(t.Context(), localInboxToolName, inboxArgs(nil))
	if err == nil || value != "" || strings.Contains(err.Error(), "synthetic-secret") || reads != 1 {
		t.Fatalf("unknown response: %q %v", value, err)
	}
	// Reopen the complete handler and its private database, not just a tool.
	f.server.Close()
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(douyinapp.New(filepath.Join(f.dir, "apps", "douyin")).Registration())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(f.dir, f.root, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	f = newLocalAppObservationFixtureWithHandler(t, handler, f.dir, f.root)
	f.login()
	f.handler.home.douyinWeb.available = func() bool { return true }
	f.handler.home.douyinWeb.readNotice = m.readNotice
	tools = noticeToolSet(t, f, "notice-unknown", false)
	value, err = tools.Call(t.Context(), localInboxToolName, inboxArgs(nil))
	if err != nil || !strings.Contains(value, "unknown_not_repeated") || reads != 1 {
		t.Fatalf("unknown read replayed: %q %v", value, err)
	}
}

func TestLocalInboxCursorCannotCrossRequestOrHTTPAndRevocationDiscardsRead(t *testing.T) {
	f, m, _ := syntheticNoticeFixture(t, true)
	reads := 0
	revoke := false
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		if revoke {
			setSyntheticNoticeAgent(t, f, false)
		}
		return stationbrowser.DouyinNoticePage{Account: in.Account, HasMore: true, PagesRead: 1, Next: stationbrowser.DouyinNoticeCheckpoint{Pages: 1, Digest: [32]byte{8}}, RenderedText: "private synthetic notice"}, nil
	}
	tools := noticeToolSet(t, f, "notice-original", true)
	value, err := tools.Call(t.Context(), localInboxToolName, inboxArgs(nil))
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if json.Unmarshal([]byte(value), &page) != nil || page["continuation"] == "" {
		t.Fatal(value)
	}
	cursor := page["continuation"]
	other := noticeToolSet(t, f, "notice-other", true)
	if value, err := other.Call(t.Context(), localInboxToolName, inboxArgs(map[string]any{"continuation": cursor})); err == nil || value != "" || reads != 1 {
		t.Fatalf("cursor crossed requests: %q %v", value, err)
	}
	_, view := f.request("GET", localDouyinWebRoute, nil, false)
	if code, _ := f.request("POST", localDouyinNoticePath, map[string]any{"action": "prepare", "revision": view["revision"], "allow_mark_read": true, "continuation": cursor}, true); code != 409 || reads != 1 {
		t.Fatalf("tool cursor crossed into HTTP: %d", code)
	}
	revoke = true
	if value, err := other.Call(t.Context(), localInboxToolName, inboxArgs(nil)); err == nil || value != "" || reads != 2 {
		t.Fatalf("late revoked body escaped: %q %v", value, err)
	}
	if tools.checkAppObservations(t.Context()) == nil {
		t.Fatal("old notice proof survived revocation")
	}
}

func TestLocalInboxRevocationBeforeReplyPreventsChatAcceptance(t *testing.T) {
	f, m, _ := syntheticNoticeFixture(t, true)
	reads, calls := 0, 0
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, RenderedText: "合成撤权正文"}, check()
	}
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return &syntheticObservationModel{call: func(_ context.Context, _ string, _ []map[string]string, _ []mcptools.Tool) (string, error) {
			calls++
			if calls == 1 {
				return localToolCall("discover_tools", map[string]any{"query": localInboxToolName}), nil
			}
			if calls == 2 {
				return localToolCall(localInboxToolName, map[string]any{"action": "read", "app_id": "douyin", "allow_mark_read": true}), nil
			}
			setSyntheticNoticeAgent(t, f, false)
			return `{"action":"reply","messages":["合成撤权正文"]}`, nil
		}}, nil
	}
	code, out := localChatSend(f, "notice-revoke-at-reply", "读取互动通知，允许标记已读")
	if code != 409 || reads != 1 || strings.Contains(localAppObservationJSONString(out), "合成撤权正文") {
		t.Fatalf("revoked reply accepted: %d %v reads=%d", code, out, reads)
	}
}

func TestLocalInboxReloginDetachAndRemovalResetAgentGrant(t *testing.T) {
	for _, action := range []string{"begin", "begin_creator", "begin_creator_works", "detach", "clear", "revoke"} {
		t.Run(action, func(t *testing.T) {
			f, _, view := syntheticNoticeFixture(t, true)
			if action == "begin_creator" || action == "begin_creator_works" {
				preparation := map[string]any{"action": "set_publish", "revision": view["revision"], "allow_publish": true}
				if action == "begin_creator_works" {
					preparation = map[string]any{"action": "set_works", "revision": view["revision"], "allow_works": true}
				}
				code, next := f.request("POST", localDouyinWebRoute, preparation, true)
				if code != 200 {
					t.Fatal(code, next)
				}
				view = next
			}
			path := localDouyinWebRoute
			payload := map[string]any{"action": action, "revision": view["revision"]}
			if action == "begin" || action == "clear" {
				payload["approve_profile"] = true
			}
			if action == "revoke" {
				path = localDouyinNoticePath
			}
			code, view := f.request("POST", path, payload, true)
			if code != 200 || view["notice_agent_enabled"] != false || view["notice_bound"] != false {
				t.Fatalf("%s retained old grant: %d %v", action, code, view)
			}
		})
	}
}

func TestLocalInboxForgottenSourceCannotRead(t *testing.T) {
	f, m, _ := syntheticNoticeFixture(t, true)
	reads := 0
	m.readNotice = func(context.Context, string, stationbrowser.DouyinNoticeRequest, func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		return stationbrowser.DouyinNoticePage{}, nil
	}
	tools := noticeToolSet(t, f, "notice-forgotten", true)
	if err := tools.store.Clear(companion.ScopeID("local", tools.access.fishID, tools.access.ownerID)); err != nil {
		t.Fatal(err)
	}
	if value, err := tools.Call(t.Context(), localInboxToolName, inboxArgs(nil)); err == nil || value != "" || reads != 0 {
		t.Fatalf("forgotten request read notifications: %q %v", value, err)
	}
}
