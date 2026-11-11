package station

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

func surfAppFixture(t *testing.T) (*surfShareFixture, *localDouyinWebLogin, string) {
	t.Helper()
	x := newSurfShareFixture(t, douyinapp.NewWebOnly(nil).Registration())
	m := x.f.handler.home.douyinWeb
	m.available = func() bool { return true }
	m.open = func(ctx context.Context, _ string) (localDouyinLoginWindow, error) {
		w := new(syntheticDouyinLoginWindow)
		w.active.Store(true)
		context.AfterFunc(ctx, func() { w.active.Store(false) })
		return w, nil
	}
	view := enableSyntheticDouyinComments(t, x.f, enableSyntheticDouyinWebRead(t, x.f), true)
	x.request("POST", localDouyinWebRoute, map[string]any{"action": "set_interactions", "revision": view["revision"], "allow_likes": true, "allow_replies": true}, 200)
	syntheticDouyinWebPlaybackTarget(t, x.f, m)
	m.readVideo = func(context.Context, string, string, func() error) (stationbrowser.DouyinWebPage, error) {
		return stationbrowser.DouyinWebPage{Text: "合成视频详情"}, nil
	}
	m.readComments = func(_ context.Context, _ string, _ stationbrowser.DouyinCommentsRequest, check func() error) (stationbrowser.DouyinCommentPage, error) {
		if err := check(); err != nil {
			return stationbrowser.DouyinCommentPage{}, err
		}
		return stationbrowser.DouyinCommentPage{Comments: []stationbrowser.DouyinComment{{ID: "101", AuthorID: "synthetic-commenter", Text: "合成评论"}}, Complete: true}, nil
	}
	available := x.request("GET", x.path+"/surf-app-grants", nil, 200)
	if available["current"] != false || len(available["scopes"].([]any)) != 0 {
		t.Fatal("app scope was inherited", available)
	}
	choices := available["available"].([]any)
	if len(choices) != 1 {
		t.Fatal("expected one app root", available)
	}
	id := choices[0].(map[string]any)["id"].(string)
	if code, _ := x.f.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 0, "key": "csrf", "scopes": []any{}}, false); code != 403 {
		t.Fatal("app grant bypassed CSRF", code)
	}
	x.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 0, "key": "allow-app", "scopes": []any{map[string]any{"id": id, "fields": []string{"page_text", "comments"}, "actions": []string{"like", "reply_comment"}}}}, 200)
	if _, err := x.worker.updateSurf("one", stationSurfUpdate{Action: "resume", Revision: 1, Key: "resume-apps", AllowPublicReads: true}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	return x, m, id
}

func surfAppRun(t *testing.T, x *surfShareFixture, trace string) (*stationSurfTools, context.Context) {
	t.Helper()
	state, err := x.worker.surfSchedule("one")
	if err != nil {
		t.Fatal(err)
	}
	factory, err := x.host.prepareSurf(t.Context(), x.worker, state, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	access, err := factory(t.Context(), trace)
	if err != nil {
		t.Fatal(err)
	}
	tools := access.(*stationSurfTools)
	ctx, stop, err := tools.BindSurfBrowseContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	tools.SetTrace(companion.Input{Channel: "host", Account: "one", Thread: "host:surf:one", TraceID: trace, Mode: "direct", Preview: true, Surf: true})
	if tools.appGrant == nil {
		t.Fatal("explicit app grant not attached")
	}
	return tools, ctx
}

func surfAppCall(t *testing.T, tools *stationSurfTools, ctx context.Context, input map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(input)
	text, err := tools.Call(ctx, stationSurfAppTool, raw)
	if err != nil {
		t.Fatalf("surf_app %s: %v", raw, err)
	}
	var out map[string]any
	if json.Unmarshal([]byte(text), &out) != nil {
		t.Fatal(text)
	}
	return out
}

func surfAppReadVideo(t *testing.T, tools *stationSurfTools, ctx context.Context, scope string) string {
	t.Helper()
	surfAppCall(t, tools, ctx, map[string]any{"action": "targets"})
	page := surfAppCall(t, tools, ctx, map[string]any{"action": "read", "scope_id": scope, "content_id": "home", "field": "page_text", "limit": 2048, "refresh": true})
	ids := localWebFollowUps(page)
	if len(ids) != 1 {
		t.Fatal("root did not return verified followup", page)
	}
	id := ids[0]
	surfAppCall(t, tools, ctx, map[string]any{"action": "read", "scope_id": scope, "content_id": id, "field": "page_text", "limit": 2048})
	return id
}

func TestStationSurfAppsReadLikeReceiptsAndRevocation(t *testing.T) {
	x, m, scope := surfAppFixture(t)
	tools, ctx := surfAppRun(t, x, "surf-app-one")
	clicks := 0
	m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, check func() error) (stationbrowser.DouyinInteractionResult, error) {
		if in.Submitted {
			return stationbrowser.DouyinInteractionResult{State: "applied", RemoteID: "like:" + in.VideoID, Submitted: true, Code: "applied"}, nil
		}
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		if err := check(); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		clicks++
		return stationbrowser.DouyinInteractionResult{State: "applied", RemoteID: "like:" + in.VideoID, Submitted: true, Code: "applied"}, nil
	}
	bad, _ := json.Marshal(map[string]any{"action": "create", "scope_id": scope, "content_id": "guessed-video", "kind": "like", "desired": true})
	if _, err := tools.Call(ctx, stationSurfAppTool, bad); err == nil || clicks != 0 {
		t.Fatal("unread target allowed", err)
	}
	id := surfAppReadVideo(t, tools, ctx, scope)
	created := surfAppCall(t, tools, ctx, map[string]any{"action": "create", "scope_id": scope, "content_id": id, "kind": "like", "desired": true})
	job := created["job_id"].(string)
	if clicks != 0 {
		t.Fatal("create performed interaction")
	}
	result := surfAppCall(t, tools, ctx, map[string]any{"action": "submit", "job_id": job})
	if result["state"] != "applied" || clicks != 1 || len(x.sends) != 0 {
		t.Fatal("platform receipt or zero WeChat sends", result, clicks)
	}
	page := x.request("GET", "/api/local/interactions?app_id=douyin", nil, 200)
	if !strings.Contains(fmt.Sprint(page), job) {
		t.Fatal("app modal ledger lost surf task", page)
	}
	// A later independent run has no old model context. Re-reading the public
	// entry and choosing the same action reuses the original task.
	second, ctx2 := surfAppRun(t, x, "surf-app-two")
	id2 := surfAppReadVideo(t, second, ctx2, scope)
	duplicate := surfAppCall(t, second, ctx2, map[string]any{"action": "create", "scope_id": scope, "content_id": id2, "kind": "like", "desired": true})
	if duplicate["job_id"] != job || clicks != 1 {
		t.Fatal("next round duplicated action", duplicate, clicks)
	}
	x.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 1, "key": "revoke-app", "scopes": []any{}}, 200)
	if _, err := second.Call(ctx2, stationSurfAppTool, json.RawMessage(`{"action":"targets"}`)); err == nil {
		t.Fatal("revocation did not invalidate active model evidence")
	}
	if code, out := x.f.request("POST", "/api/local/interactions", map[string]string{"action": "status", "job_id": job}, true); code != 200 || out["state"] != "applied" {
		t.Fatal("revocation lost accepted receipt", code, out)
	}
}

func TestStationSurfAppsReplyNeedsCurrentReadAndPauseBlocksSubmit(t *testing.T) {
	x, _, scope := surfAppFixture(t)
	tools, ctx := surfAppRun(t, x, "surf-app-comment")
	id := surfAppReadVideo(t, tools, ctx, scope)
	input := map[string]any{"action": "create", "scope_id": scope, "content_id": id, "kind": "reply_comment", "comment_id": "101", "text": "合成回复"}
	raw, _ := json.Marshal(input)
	if _, err := tools.Call(ctx, stationSurfAppTool, raw); err == nil {
		t.Fatal("unread comment could be replied")
	}
	surfAppCall(t, tools, ctx, map[string]any{"action": "read", "scope_id": scope, "content_id": id, "field": "comments", "limit": 20})
	created := surfAppCall(t, tools, ctx, input)
	job := created["job_id"].(string)
	state, err := x.worker.surfSchedule("one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.worker.updateSurf("one", stationSurfUpdate{Action: "pause", Revision: state.Revision, Key: "pause-before-interaction"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code, _ := x.f.request("POST", "/api/local/interactions", map[string]string{"action": "submit", "job_id": job}, true); code == 200 {
		t.Fatal("paused surf task bypassed durable submit guard")
	}
	if code, out := x.f.request("POST", "/api/local/interactions", map[string]string{"action": "cancel", "job_id": job}, true); code != 200 || out["state"] != "cancelled" {
		t.Fatal("paused task could not be cancelled", code, out)
	}
}

func TestStationSurfAppsNormalModelReplyPacingAndUnknown(t *testing.T) {
	x, m, scope := surfAppFixture(t)
	known := syntheticDouyinWebPlaybackTarget(t, x.f, m)
	tools, ctx := surfAppRun(t, x, "surf-app-model")
	clicks := 0
	m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, check func() error) (stationbrowser.DouyinInteractionResult, error) {
		if in.Submitted {
			return stationbrowser.DouyinInteractionResult{State: "unknown", Submitted: true, Code: "receipt_pending"}, nil
		}
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		if err := check(); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		clicks++
		return stationbrowser.DouyinInteractionResult{State: "unknown", Submitted: true, Code: "receipt_pending"}, nil
	}
	job := ""
	calls := 0
	model := &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, available []mcptools.Tool) (string, error) {
		calls++
		args := map[string]any{}
		switch calls {
		case 1:
			return `{"action":"tool","tool":"discover_tools","arguments":{"query":"surf_app"}}`, nil
		case 2:
			if !hasTool(available, stationSurfAppTool) {
				t.Fatal("scoped app tool not discoverable")
			}
			args["action"] = "targets"
		case 3:
			args = map[string]any{"action": "read", "scope_id": scope, "content_id": "home", "field": "page_text"}
		case 4:
			args = map[string]any{"action": "read", "scope_id": scope, "content_id": known, "field": "comments", "limit": 20}
		case 5:
			if !strings.Contains(localModelHistory(turns), "合成评论") {
				t.Fatal("read comment missing from model")
			}
			args = map[string]any{"action": "create", "scope_id": scope, "content_id": known, "kind": "reply_comment", "comment_id": "101", "text": "合成回合回复"}
		case 6:
			pattern := regexp.MustCompile(`"job_id"\s*:\s*"([a-f0-9]{64})"`)
			for _, turn := range turns {
				var feedback struct {
					Result string `json:"result"`
				}
				_ = json.Unmarshal([]byte(turn["content"]), &feedback)
				if match := pattern.FindStringSubmatch(feedback.Result); len(match) > 1 {
					job = match[1]
				}
			}
			if job == "" {
				t.Fatal("model did not receive original job id", localModelHistory(turns))
			}
			args = map[string]any{"action": "submit", "job_id": job}
		default:
			return `{"action":"silent","reason":"surf_browse_only"}`, nil
		}
		raw, _ := json.Marshal(map[string]any{"action": "tool", "tool": stationSurfAppTool, "arguments": args})
		return string(raw), nil
	}}
	config := companion.Defaults()
	config.Enabled = true
	config.QuietStart, config.QuietEnd = "00:00", "00:00"
	config.Permissions = companion.Permissions{ReplyText: true}
	engine, err := companion.NewPreview(config, model, "合成应用冲浪")
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = tools
	engine.MaxToolCalls = 12
	decision, err := engine.Respond(ctx, companion.Input{Channel: "host", Account: "one", Thread: "host:surf:one", TraceID: "surf-app-model", Mode: "direct", Preview: true, Surf: true, Text: "通过surf_app按本轮授权浏览和互动"})
	if err != nil || decision.Action != "silent" || clicks != 1 || job == "" || len(x.sends) != 0 {
		t.Fatal("normal model path failed", decision, err, clicks, job)
	}
	// A later round re-reads the comment but must reconcile the original
	// unknown task, not generate another reply with a different text.
	second, ctx2 := surfAppRun(t, x, "surf-app-model-later")
	id := surfAppReadVideo(t, second, ctx2, scope)
	surfAppCall(t, second, ctx2, map[string]any{"action": "read", "scope_id": scope, "content_id": id, "field": "comments", "limit": 20})
	reused := surfAppCall(t, second, ctx2, map[string]any{"action": "create", "scope_id": scope, "content_id": id, "kind": "reply_comment", "comment_id": "101", "text": "不得替换未知任务的原说明"})
	if reused["job_id"] != job || clicks != 1 {
		t.Fatal("unknown reply was duplicated", reused, clicks)
	}
	provider := &localDouyinWebInteraction{home: x.f.handler.home}
	a, err := provider.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.replyPacing(a, time.Now(), douyinReplyCheck); err == nil {
		t.Fatal("surf reply bypassed normal persistent random interval")
	}
}

func TestStationSurfAppsWithoutPublicReader(t *testing.T) {
	x, _, scope := surfAppFixture(t)
	if _, err := x.web.store.SetEnabled(t.Context(), stationSurfPlugin, x.installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	state, err := x.worker.surfSchedule("one")
	if err != nil {
		t.Fatal(err)
	}
	settings := state.Settings
	settings.Sources = nil
	x.request("PUT", x.path+"/surf", stationSurfUpdate{Action: "save", Revision: state.Revision, Key: "apps-only", Settings: &settings}, 200)
	state, err = x.worker.surfSchedule("one")
	if err != nil {
		t.Fatal(err)
	}
	ready := x.request("GET", x.path+"/surf", nil, 200)
	if ready["public_reader_ready"] != false || ready["application_reader_ready"] != true {
		t.Fatal("app-only readiness still requires web reader", ready)
	}
	x.request("PUT", x.path+"/surf", stationSurfUpdate{Action: "resume", Revision: state.Revision, Key: "start-apps-only", AllowPublicReads: true}, 200)
	tools, ctx := surfAppRun(t, x, "surf-apps-only")
	list, err := tools.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != stationSurfAppTool {
		t.Fatal("app-only tool set", list, err)
	}
	if _, err := tools.Call(ctx, "web_fetch", json.RawMessage(`{}`)); err == nil {
		t.Fatal("app grant enabled web fetch")
	}
	surfAppReadVideo(t, tools, ctx, scope)
	state, err = x.worker.surfSchedule("one")
	if err != nil || state.LastRun.ApplicationReads < 2 {
		t.Fatal("app progress metadata not persisted", state, err)
	}
	x.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 1, "key": "no-app-entry", "scopes": []any{}}, 200)
	if _, err := x.worker.updateSurf("one", stationSurfUpdate{Action: "pause", Revision: state.Revision, Key: "stop-empty-surf"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	state, err = x.worker.surfSchedule("one")
	if err != nil {
		t.Fatal(err)
	}
	x.request("PUT", x.path+"/surf", stationSurfUpdate{Action: "resume", Revision: state.Revision, Key: "deny-empty-surf", AllowPublicReads: true}, 409)
}

func TestStationSurfAppsGrantConflictsAndOfflineRevoke(t *testing.T) {
	x, _, scope := surfAppFixture(t)
	current := x.request("GET", x.path+"/surf-app-grants", nil, 200)
	if current["current"] != true {
		t.Fatal("saved app grant not current", current)
	}
	for _, private := range []string{"\"Owner\"", "\"Dataset\"", "\"Request\"", "web-login/"} {
		if strings.Contains(localAppObservationJSONString(current), private) {
			t.Fatal("private grant leaked", private)
		}
	}
	x.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 0, "key": "stale", "scopes": []any{}}, 409)
	x.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 1, "key": "no-root", "scopes": []any{map[string]any{"id": scope, "fields": []string{"comments"}, "actions": []string{}}}}, 400)
	x.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 1, "key": "bad-action", "scopes": []any{map[string]any{"id": scope, "fields": []string{"page_text"}, "actions": []string{"publish"}}}}, 409)
	x.mu.Lock()
	x.dataset = "synthetic-offline-replacement"
	x.mu.Unlock()
	out := x.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 1, "key": "offline-clear", "scopes": []any{}}, 200)
	if out["revision"] != float64(2) || out["current"] != false || len(out["scopes"].([]any)) != 0 {
		t.Fatal("offline revocation failed", out)
	}
}
