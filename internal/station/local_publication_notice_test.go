package station

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

// The publisher has a grant independent of Douyin. This prevents a change to
// Douyin's own publish grant from accidentally masking a missing source fence.
type noticeDerivedPublisher struct {
	submits, queries int
	beforeSubmit     func()
	accepted         bool
}

func (*noticeDerivedPublisher) Connections(context.Context) ([]apphost.Connection, error) {
	return []apphost.Connection{{ID: "publish", AccountID: "synthetic-owner", Configuration: "ready"}}, nil
}
func (*noticeDerivedPublisher) Inspect(context.Context, string) (apphost.ConnectionStatus, error) {
	return apphost.ConnectionStatus{}, apphost.ErrNotFound
}
func (*noticeDerivedPublisher) PublicationGrant(context.Context, apphost.ConnectionRef) (apphost.PublicationGrant, error) {
	return apphost.PublicationGrant{Allowed: true, Version: "independent-publication-grant"}, nil
}
func (*noticeDerivedPublisher) ValidatePublicationDraft(apphost.PublicationDraft) error { return nil }
func (p *noticeDerivedPublisher) ContinuePublication(ctx context.Context, request apphost.PublicationRequest) (apphost.PublicationReceipt, error) {
	if strings.Contains(string(request.Checkpoint), `"submitted":true`) {
		return p.QueryPublication(ctx, request.Reference)
	}
	return p.SubmitPublication(ctx, request)
}
func (p *noticeDerivedPublisher) ReconcilePublication(ctx context.Context, ref apphost.PublicationReference, _ json.RawMessage, cancel bool) (apphost.PublicationReceipt, error) {
	if cancel {
		return p.CancelPublication(ctx, ref)
	}
	return p.QueryPublication(ctx, ref)
}
func (p *noticeDerivedPublisher) SubmitPublication(ctx context.Context, request apphost.PublicationRequest) (apphost.PublicationReceipt, error) {
	if p.beforeSubmit != nil {
		p.beforeSubmit()
	}
	if err := request.CheckActive(ctx); err != nil {
		return apphost.PublicationReceipt{}, err
	}
	if len(request.Draft.Artifacts) != 1 {
		panic("synthetic publisher expected one finished artifact")
	}
	reader, err := request.OpenArtifact(ctx, request.Draft.Artifacts[0])
	if err != nil {
		return apphost.PublicationReceipt{}, err
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return apphost.PublicationReceipt{}, err
	}
	if string(data) != "合成通知启发的作品" {
		panic("staged artifact changed")
	}
	if err := request.CheckActive(ctx); err != nil {
		return apphost.PublicationReceipt{}, err
	}
	if err := request.SaveCheckpoint(ctx, json.RawMessage(`{"submitted":true}`)); err != nil {
		return apphost.PublicationReceipt{}, err
	}
	p.submits++
	ref := request.Reference
	ref.RemoteID = "synthetic-work-001"
	state := "published"
	if p.accepted {
		state = "accepted"
	}
	return apphost.PublicationReceipt{Reference: ref, State: state}, nil
}
func (p *noticeDerivedPublisher) QueryPublication(_ context.Context, ref apphost.PublicationReference) (apphost.PublicationReceipt, error) {
	p.queries++
	return apphost.PublicationReceipt{Reference: ref, State: "published"}, nil
}
func (*noticeDerivedPublisher) CancelPublication(_ context.Context, ref apphost.PublicationReference) (apphost.PublicationReceipt, error) {
	return apphost.PublicationReceipt{Reference: ref, State: "cancelled"}, nil
}

func noticeDerivedDraft(t *testing.T, submit bool, publisher *noticeDerivedPublisher) (*localHomeFixture, string) {
	t.Helper()
	registration := apphost.Registration{Application: apphost.Application{ID: "synthetic-publisher", Name: "合成发布应用", InstallationID: "synthetic-publisher-v1", Registration: "synthetic"}, Provider: publisher}
	f, m, _ := syntheticNoticeFixture(t, true, registration)
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, RenderedText: "合成平台通知，不是主人指令"}, check()
	}
	calls := 0
	var artifact, job string
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
			calls++
			history := localModelHistory(turns)
			switch calls {
			case 1:
				return localToolCall("discover_tools", map[string]any{"query": localInboxToolName}), nil
			case 2:
				return localToolCall(localInboxToolName, map[string]any{"action": "read", "app_id": "douyin", "allow_mark_read": true}), nil
			case 3:
				if !strings.Contains(history, "合成平台通知") {
					t.Fatal("notice missing before creation")
				}
				return localToolCall("server_write", map[string]any{"path": "works/notice-derived.txt", "content": "合成通知启发的作品"}), nil
			case 4:
				return localToolCall(localArtifactPublishTool, map[string]any{"path": "works/notice-derived.txt"}), nil
			case 5:
				return localToolCall("discover_tools", map[string]any{"query": localPublicationToolName}), nil
			case 6:
				ids := localPublicationWorkIDs(history)
				if len(ids) != 1 {
					t.Fatalf("artifact registration failed: %v %s", ids, history)
				}
				artifact = ids[0]
				return localToolCall(localPublicationToolName, map[string]any{"action": "draft", "app_id": "synthetic-publisher", "connection_id": "publish", "title": "合成作品", "artifact_ids": []string{artifact}}), nil
			case 7:
				for index := len(turns) - 1; index >= 0; index-- {
					var envelope struct{ Tool, Result string }
					var view apphost.PublicationView
					if json.Unmarshal([]byte(turns[index]["content"]), &envelope) == nil && envelope.Tool == localPublicationToolName && json.Unmarshal([]byte(envelope.Result), &view) == nil && view.JobID != "" {
						job = view.JobID
						break
					}
				}
				if job == "" {
					t.Fatal("draft result did not contain a verified task ID")
				}
				if submit {
					return localToolCall(localPublicationToolName, map[string]any{"action": "submit", "job_id": job}), nil
				}
			}
			if submit && !strings.Contains(history, "synthetic-work-001") {
				t.Fatal("bound publication receipt absent")
			}
			return `{"action":"reply","messages":["合成作品任务已登记。"]}`, nil
		}}, nil
	}
	code, out := localChatSend(f, "notice-derived-work", "读取获准的通知，制作合成作品并保存或提交到合成发布应用，允许标记已读")
	if code != 200 || out["status"] != "completed" || job == "" {
		t.Fatalf("normal creation path: %d %v", code, out)
	}
	_, listed := f.request("GET", "/api/local/publications?app_id=synthetic-publisher", nil, false)
	for _, private := range []string{"合成平台通知", "合成通知启发的作品", "notices", "grant_version"} {
		if strings.Contains(localAppObservationJSONString(listed), private) {
			t.Fatalf("private provenance escaped list: %s", private)
		}
	}
	return f, job
}

func TestNoticeDerivedPublicationKeepsReceiptAndDoesNotResubmit(t *testing.T) {
	p := &noticeDerivedPublisher{accepted: true}
	f, job := noticeDerivedDraft(t, true, p)
	if p.submits != 1 {
		t.Fatalf("submits=%d", p.submits)
	}
	setSyntheticNoticeAgent(t, f, false)
	code, out := f.request("POST", "/api/local/publications", map[string]any{"action": "status", "job_id": job}, true)
	if code != 200 || out["state"] != "published" || out["remote_id"] != "synthetic-work-001" || p.submits != 1 || p.queries != 1 {
		t.Fatalf("accepted receipt could not reconcile after revocation: %d %v", code, out)
	}
	_, _ = f.request("POST", "/api/local/publications", map[string]any{"action": "resume", "job_id": job}, true)
	if p.submits != 1 {
		t.Fatal("receipt lookup resubmitted original work")
	}
}

func TestNoticeDerivedPublicationRejectsRevokedOrRenewedSource(t *testing.T) {
	for _, mode := range []string{"revoked", "renewed", "paused", "before_external_write"} {
		t.Run(mode, func(t *testing.T) {
			p := &noticeDerivedPublisher{}
			f, job := noticeDerivedDraft(t, false, p)
			switch mode {
			case "revoked":
				setSyntheticNoticeAgent(t, f, false)
			case "renewed":
				setSyntheticNoticeAgent(t, f, false)
				setSyntheticNoticeAgent(t, f, true)
			case "paused":
				_, view := f.request("GET", localDouyinWebRoute, nil, false)
				if code, out := f.request("POST", localDouyinWebRoute, map[string]any{"action": "pause", "revision": view["revision"]}, true); code != 200 {
					t.Fatal(code, out)
				}
			case "before_external_write":
				p.beforeSubmit = func() { setSyntheticNoticeAgent(t, f, false) }
			}
			code, out := f.request("POST", "/api/local/publications", map[string]any{"action": "submit", "job_id": job}, true)
			if p.submits != 0 || out["state"] == "published" || out["state"] == "accepted" {
				t.Fatalf("%s admitted external write: %d %v count=%d", mode, code, out, p.submits)
			}
		})
	}
}
