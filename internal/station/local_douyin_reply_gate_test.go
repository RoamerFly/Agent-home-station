package station

import (
	"context"
	"encoding/json"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

// A pending job created by a pre-gate version must remain inspectable and
// cancellable without gaining a new submission path after the upgrade.
func TestLocalDouyinGenericReplyGateKeepsOldJobsReadOnly(t *testing.T) {
	f, manager, contentID, _ := syntheticInteractionFixture(t, true)
	reader := &localDouyinWebObservation{home: manager.home}
	access, err := reader.state(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	target, ok := reader.resolveTarget(access, contentID)
	if !ok || target.videoID == "" {
		t.Fatal("synthetic video target unavailable")
	}
	if err := reader.saveCommentProof(access, target.videoID, stationbrowser.DouyinComment{
		ID: "105", AuthorID: "other-author", Text: "旧版已读评论",
	}); err != nil {
		t.Fatal(err)
	}
	host, actor, err := f.handler.chat.interactionHost()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := f.handler.chat.projectTools.forFish(actor.Account)
	if err != nil {
		t.Fatal(err)
	}
	legacy := apphost.NewInteractor(f.handler.chat.applications, backend.store, nil)
	ref, err := legacy.ResolveTarget(t.Context(), douyinapp.ID, douyinapp.WebReadConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	comment, err := reader.commentProof(access, target.videoID, "105")
	if err != nil {
		t.Fatal(err)
	}
	draft := apphost.InteractionDraft{Target: ref, ContentID: contentID, Kind: "reply_comment",
		CommentID: "105", CommentVersion: comment.SHA256, Text: "旧版合成回复", Origin: json.RawMessage(`{}`)}
	clicks := 0
	manager.interact = func(context.Context, string, stationbrowser.DouyinInteractionRequest, func() error) (stationbrowser.DouyinInteractionResult, error) {
		clicks++
		return stationbrowser.DouyinInteractionResult{}, nil
	}
	actor.RequestKey = "legacy-pending-reply"
	old, err := legacy.Create(t.Context(), actor, draft)
	if err != nil || old.State != "pending" {
		t.Fatalf("synthetic pre-gate job: %+v %v", old, err)
	}
	if _, err := host.Action(t.Context(), actor, old.JobID, "submit"); err == nil {
		t.Fatal("old pending reply crossed the server submission gate")
	}
	if status, err := host.Action(t.Context(), actor, old.JobID, "status"); err != nil || status.JobID != old.JobID || status.State != "pending" {
		t.Fatalf("old reply status unavailable: %+v %v", status, err)
	}
	checkActions := func(label string, value map[string]any) {
		t.Helper()
		for _, raw := range value["available_actions"].([]any) {
			if raw == "submit" || raw == "resume" {
				t.Fatalf("%s advertised a blocked reply action: %v", label, value)
			}
		}
	}
	tools := noticeToolSet(t, f, "legacy-reply-status", true)
	toolInput, _ := json.Marshal(map[string]any{"action": "status", "job_id": old.JobID})
	toolValue, err := tools.Call(t.Context(), localInteractionToolName, toolInput)
	if err != nil {
		t.Fatal(err)
	}
	var toolStatus map[string]any
	if err := json.Unmarshal([]byte(toolValue), &toolStatus); err != nil {
		t.Fatal(err)
	}
	checkActions("Agent tool", toolStatus)
	if code, page := f.request("GET", "/api/local/interactions?app_id=douyin", nil, false); code != 200 {
		t.Fatalf("old task list unavailable: %d %v", code, page)
	} else {
		items := page["tasks"].(map[string]any)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("old task missing from owner list: %v", page)
		}
		checkActions("owner task list", items[0].(map[string]any))
	}
	if code, cancelled := f.request("POST", "/api/local/interactions", map[string]any{"action": "cancel", "job_id": old.JobID}, true); code != 200 || cancelled["job_id"] != old.JobID || cancelled["state"] != "cancelled" {
		t.Fatalf("old pending reply cannot be cancelled: %d %+v", code, cancelled)
	}
	actor.RequestKey = "legacy-submitting-reply"
	old, err = legacy.Create(t.Context(), actor, draft)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := backend.store.Job(t.Context(), old.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.store.ClaimJobWithSubmission(t.Context(), old.JobID, stored.Revision, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Action(t.Context(), actor, old.JobID, "resume"); err == nil {
		t.Fatal("old submitting reply crossed the resume gate")
	}
	if clicks != 0 {
		t.Fatalf("blocked old reply reached platform adapter %d times", clicks)
	}
	for _, entry := range f.handler.chat.interactionTargets(t.Context()) {
		targetRef, ok := entry["target"].(apphost.ConnectionRef)
		if !ok || targetRef.AppID != douyinapp.ID {
			continue
		}
		for _, kind := range entry["kinds"].([]string) {
			if kind == "reply_comment" {
				t.Fatal("targets still advertised Douyin generic replies")
			}
		}
	}
}
