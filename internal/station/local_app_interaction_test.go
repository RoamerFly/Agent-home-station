package station

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcatalog "wechat-bot/connected-apps"
	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

func syntheticInteractionFixture(t *testing.T, reply bool) (*localHomeFixture, *localDouyinWebLogin, string, map[string]any) {
	t.Helper()
	f, m := newLocalDouyinWebFixture(t)
	view := enableSyntheticDouyinComments(t, f, enableSyntheticDouyinWebRead(t, f), true)
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_interactions", "revision": view["revision"], "allow_likes": true, "allow_replies": reply}, true)
	if code != 200 || view["likes_enabled"] != true || view["replies_enabled"] != reply {
		t.Fatalf("permission: %d %#v", code, view)
	}
	id := syntheticDouyinWebPlaybackTarget(t, f, m)
	return f, m, id, view
}

func TestLocalAppInteractionNormalChatCreatesSubmitsAndKeepsReceipt(t *testing.T) {
	f, m, id, _ := syntheticInteractionFixture(t, false)
	clicks := 0
	m.interact = func(ctx context.Context, profile string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
		if in.Submitted {
			t.Fatal("completed action unexpectedly queried platform")
		}
		if in.VideoID != "7000000000000000011" || in.Kind != "like" || !in.Desired {
			t.Fatal("target changed", in)
		}
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		if err := authorize(); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		clicks++
		return stationbrowser.DouyinInteractionResult{State: "applied", RemoteID: "like:" + in.VideoID, Submitted: true, Code: "applied"}, nil
	}
	calls := 0
	job := ""
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
			calls++
			switch calls {
			case 1:
				return localToolCall("discover_tools", map[string]any{"query": "app_action"}), nil
			case 2:
				return localToolCall("app_action", map[string]any{"action": "create", "app_id": "douyin", "connection_id": "web-page-text", "content_id": id, "kind": "like", "desired": true}), nil
			case 3:
				ids := localPublicationJobIDs(localModelHistory(turns))
				if len(ids) != 1 {
					t.Fatal("missing interaction task", localModelHistory(turns))
				}
				job = ids[0]
				return localToolCall("app_action", map[string]any{"action": "submit", "job_id": job}), nil
			default:
				if !strings.Contains(localModelHistory(turns), `applied`) {
					t.Fatal("verified receipt not in model context")
				}
				return `{"action":"reply","messages":["已核对点赞状态。"]}`, nil
			}
		}}, nil
	}
	if code, out := localChatSend(f, "interaction-like-normal", "给这条视频点个赞"); code != 200 || out["status"] != "completed" || clicks != 1 {
		t.Fatalf("chat: %d %#v clicks=%d", code, out, clicks)
	}
	if code, _ := localChatSend(f, "interaction-like-normal", "给这条视频点个赞"); code != 200 || clicks != 1 || calls != 4 {
		t.Fatal("replay resubmitted action")
	}
	if code, _ := f.request("POST", "/api/local/interactions", map[string]any{"action": "status", "job_id": job}, false); code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if code, out := f.request("POST", "/api/local/interactions", map[string]any{"action": "status", "job_id": job}, true); code != 200 || out["state"] != "applied" {
		t.Fatal("receipt not durable", code, out)
	}
	code, page := f.request("GET", "/api/local/interactions?app_id=douyin", nil, false)
	if code != 200 || len(page["tasks"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatal("ledger not visible", code, page)
	}
	if strings.Contains(localAppObservationJSONString(page), "origin") || strings.Contains(localAppObservationJSONString(page), "给这条视频") {
		t.Fatal("source escaped metadata view")
	}
	_, pub := f.request("GET", "/api/local/publications?app_id=douyin", nil, false)
	if len(pub["tasks"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatal("interaction appeared as publication")
	}

	_, login := f.request("GET", localDouyinWebRoute, nil, false)
	_, home := f.request("GET", "/api/local/home", nil, false)
	contract, err := appcatalog.Lookup(douyinapp.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.handler.home.appCatalog, err = filepath.Abs("../../connected-apps")
	if err != nil {
		t.Fatal(err)
	}
	code, checkout := f.request("POST", "/api/local/apps/douyin/install", map[string]any{"revision": home["revision"], "digest": contract.Digest}, true)
	if code != http.StatusOK {
		t.Fatal("install checkout", code, checkout)
	}
	marker := filepath.Join(checkout["directory"].(string), "synthetic-edits.md")
	if err = os.WriteFile(marker, []byte("local changes"), 0600); err != nil {
		t.Fatal(err)
	}
	detach := map[string]any{"action": "detach", "revision": login["revision"]}
	if code, _ = f.request("POST", localDouyinWebRoute, detach, false); code != http.StatusForbidden {
		t.Fatal("detach accepted without CSRF", code)
	}
	code, login = f.request("POST", localDouyinWebRoute, detach, true)
	if code != http.StatusOK || login["detached"] != true || login["paused"] != true || login["likes_enabled"] != false {
		t.Fatal("detach did not withdraw the grants", code, login)
	}
	if code, apps := f.request("GET", "/api/apps", nil, false); code != http.StatusOK || len(apps["items"].([]any)) != 0 {
		t.Fatal("detached provider still registered", code, apps)
	}
	if code, targets := f.request("GET", "/api/local/apps/observation", nil, false); code != http.StatusOK || len(localAppObservationItems(targets)) != 0 {
		t.Fatal("detached app still provided observation targets", code, targets)
	}
	if code, out := f.request("POST", "/api/local/interactions", map[string]any{"action": "status", "job_id": job}, true); code != http.StatusOK || out["state"] != "applied" || clicks != 1 {
		t.Fatal("local receipt was lost or caused external work", code, out)
	}
	if code, out := f.request("GET", "/api/local/project-tools", nil, false); code != http.StatusOK || out["tools"] == nil {
		t.Fatal("detach affected shared tools", code, out)
	}
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(context.Context, string, []map[string]string) (string, error) {
			return `{"action":"reply","messages":["继续在家里聊天。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, "detached-app-local-chat", "继续本地聊天"); code != http.StatusOK || out["status"] != "completed" {
		t.Fatal("detach interrupted local chat", code, out)
	}
	if err = f.handler.Close(); err != nil {
		t.Fatal(err)
	}

	registry, err := apphost.New(douyinapp.NewWebOnly(nil).Registration())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(f.dir, f.root, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	handler.home.douyinWeb.available = func() bool { return true }
	next := newLocalAppObservationFixtureWithHandler(t, handler, f.dir, f.root)
	next.login()
	if code, apps := next.request("GET", "/api/apps", nil, false); code != http.StatusOK || len(apps["items"].([]any)) != 0 {
		t.Fatal("restart re-registered the detached app", code, apps)
	}
	if code, _ = next.request("POST", localDouyinWebRoute, map[string]any{"action": "resume", "revision": login["revision"]}, true); code != http.StatusConflict {
		t.Fatal("resume bypassed re-registration", code)
	}
	code, login = next.request("POST", localDouyinWebRoute, map[string]any{"action": "attach", "revision": login["revision"]}, true)
	if code != http.StatusOK || login["detached"] != false || login["paused"] != true || login["state"] != "saved" || login["likes_enabled"] != false {
		t.Fatal("re-registration restored access or lost login", code, login)
	}
	if code, apps := next.request("GET", "/api/apps", nil, false); code != http.StatusOK || len(apps["items"].([]any)) != 1 {
		t.Fatal("re-registration did not restore the provider", code, apps)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "local changes" {
		t.Fatal("re-registration changed the editable checkout", err)
	}
	if code, page := next.request("GET", "/api/local/interactions?app_id=douyin", nil, false); code != http.StatusOK || len(page["tasks"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatal("re-registration lost task history", code, page)
	}
}

func TestLocalAppReplyBindsReadCommentVersionAndRejectsUnseenSubject(t *testing.T) {
	f, m, id, _ := syntheticInteractionFixture(t, true)
	const original = "原评论的完整合成内容"
	m.readComments = func(context.Context, string, stationbrowser.DouyinCommentsRequest, func() error) (stationbrowser.DouyinCommentPage, error) {
		return stationbrowser.DouyinCommentPage{Comments: []stationbrowser.DouyinComment{{ID: "105", AuthorID: "author105", Author: "甲", Text: original}}, Complete: true}, nil
	}
	clicks := 0
	m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
		if in.ExpectedCommentSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(original))) || in.CommentID != "105" || in.Text != "谢谢你的分享" {
			t.Fatal("reply source lost", in)
		}
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		if err := authorize(); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		clicks++
		return stationbrowser.DouyinInteractionResult{State: "applied", RemoteID: "501", Submitted: true}, nil
	}
	calls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
			calls++
			switch calls {
			case 1:
				return localToolCall("discover_tools", map[string]any{"query": "app_action"}), nil
			case 2:
				return localToolCall("app_action", map[string]any{"action": "create", "app_id": "douyin", "connection_id": "web-page-text", "content_id": id, "kind": "reply_comment", "comment_id": "105", "text": "谢谢你的分享"}), nil
			case 3:
				if !strings.Contains(localModelHistory(turns), "先读取") {
					t.Fatal("unread subject feedback", localModelHistory(turns))
				}
				return localToolCall("discover_tools", map[string]any{"query": "app_observe"}), nil
			case 4:
				return localToolCall("app_observe", map[string]any{"action": "read", "app_id": "douyin", "connection_id": "web-page-text", "content_id": id, "field": "comments"}), nil
			case 5:
				return localToolCall("app_action", map[string]any{"action": "create", "app_id": "douyin", "connection_id": "web-page-text", "content_id": id, "kind": "reply_comment", "comment_id": "105", "text": "谢谢你的分享"}), nil
			case 6:
				ids := localPublicationJobIDs(localModelHistory(turns))
				if len(ids) != 1 {
					t.Fatal("reply task missing", localModelHistory(turns))
				}
				return localToolCall("app_action", map[string]any{"action": "submit", "job_id": ids[0]}), nil
			default:
				return `{"action":"reply","messages":["回复已核对。"]}`, nil
			}
		}}, nil
	}
	if code, out := localChatSend(f, "reply-version-normal", "回复刚才的评论"); code != 200 || clicks != 1 {
		t.Fatalf("reply: %d %#v clicks=%d", code, out, clicks)
	}
}

func TestLocalAppInteractionResumeUsesOriginalSubmissionBoundary(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		t.Run(fmt.Sprint(submitted), func(t *testing.T) {
			f, m, id, _ := syntheticInteractionFixture(t, false)
			clicks, calls := 0, 0
			m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
				calls++
				if in.Submitted {
					if in.BeforeSubmit != nil {
						t.Fatal("query retained submit callback")
					}
					return stationbrowser.DouyinInteractionResult{State: "unknown", Code: "receipt_not_found", Submitted: true}, stationbrowser.ErrDouyinInteractionUnknown
				}
				if calls == 1 && !submitted {
					return stationbrowser.DouyinInteractionResult{State: "failed", Code: "like_control_unavailable"}, stationbrowser.ErrDouyinInteractionUnavailable
				}
				if err := in.BeforeSubmit(ctx); err != nil {
					return stationbrowser.DouyinInteractionResult{}, err
				}
				clicks++
				if submitted {
					return stationbrowser.DouyinInteractionResult{State: "unknown", Code: "receipt_unknown", Submitted: true}, stationbrowser.ErrDouyinInteractionUnknown
				}
				return stationbrowser.DouyinInteractionResult{State: "applied", RemoteID: "like:" + in.VideoID, Submitted: true}, nil
			}
			modelCalls := 0
			job := ""
			f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
				return &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
					modelCalls++
					switch modelCalls {
					case 1:
						return localToolCall("discover_tools", map[string]any{"query": "app_action"}), nil
					case 2:
						return localToolCall("app_action", map[string]any{"action": "create", "app_id": "douyin", "connection_id": "web-page-text", "content_id": id, "kind": "like", "desired": true}), nil
					case 3:
						ids := localPublicationJobIDs(localModelHistory(turns))
						if len(ids) != 1 {
							t.Fatal("missing task")
						}
						job = ids[0]
						return localToolCall("app_action", map[string]any{"action": "submit", "job_id": job}), nil
					default:
						return `{"action":"reply","messages":["操作进度已保存。"]}`, nil
					}
				}}, nil
			}
			if code, out := localChatSend(f, "interaction-boundary", "给这条视频点个赞"); code != 200 {
				t.Fatal(code, out)
			}
			if code, out := f.request("POST", "/api/local/interactions", map[string]any{"action": "resume", "job_id": job}, true); code != 200 || out["job_id"] != job {
				t.Fatal("original task not resumed", code, out)
			} else if submitted && out["state"] != "unknown" {
				t.Fatal("unknown submit inferred success", out)
			} else if !submitted && out["state"] != "applied" {
				t.Fatal("pre-submit recovery failed", out)
			}
			if clicks != 1 {
				t.Fatal("duplicate or missing click", clicks)
			}
			if submitted {
				code, out := f.request("POST", "/api/local/interactions", map[string]any{"action": "cancel", "job_id": job}, true)
				if code != 200 || out["cancel_requested"] != true || clicks != 1 {
					t.Fatal("cancel lost or resubmitted", code, out)
				}
				if code, _ := f.request("POST", "/api/local/interactions", map[string]any{"action": "resume", "job_id": job}, true); code == 200 {
					t.Fatal("cancelled intent could resume")
				}
			}
		})
	}
}
