package station

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

func TestDouyinReplyPacingPersistsAndBoundsBackoff(t *testing.T) {
	f, _, _, _ := syntheticInteractionFixture(t, true)
	provider := &localDouyinWebInteraction{home: f.handler.home}
	access, err := provider.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, err := provider.replyPacing(access, now, douyinReplyReserve)
	if err != nil || first.Sub(now) < 30*time.Second || first.Sub(now) > time.Minute {
		t.Fatalf("initial spacing: %v %v", first, err)
	}
	// Reconstruct the provider and change the grant epoch without resetting
	// the reply interval for the same browser profile.
	restarted := &localDouyinWebInteraction{home: f.handler.home}
	access.record.ProfileEpoch++
	access.version = "different-grant"
	var cooling *douyinReplyCooling
	if _, err := restarted.replyPacing(access, now.Add(time.Second), douyinReplyReserve); !errors.As(err, &cooling) || !cooling.until.Equal(first) {
		t.Fatalf("restarted reservation: %v", err)
	}
	before := now.Add(time.Second)
	for i := 0; i < 7; i++ {
		until, err := restarted.replyPacing(access, before, douyinReplyRateLimited)
		if err != nil || until.Sub(before) < 90*time.Second || until.Sub(before) > 30*time.Minute {
			t.Fatalf("backoff %d: %v %v", i, until, err)
		}
		before = until
	}
	if _, err := restarted.replyPacing(access, before, douyinReplyCheck); err != nil {
		t.Fatalf("cooldown boundary: %v", err)
	}
}

func TestDouyinReplyPacingKeepsSubmittedJobQueryOnly(t *testing.T) {
	f, manager, target, _ := syntheticInteractionFixture(t, true)
	manager.readComments = func(context.Context, string, stationbrowser.DouyinCommentsRequest, func() error) (stationbrowser.DouyinCommentPage, error) {
		return stationbrowser.DouyinCommentPage{Comments: []stationbrowser.DouyinComment{{ID: "105", AuthorID: "author105", Author: "甲", Text: "合成原评论"}}, Complete: true}, nil
	}
	if code, _ := f.request("POST", "/api/local/apps/observation", map[string]string{
		"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": target, "field": "comments",
	}, true); code != 200 {
		t.Fatalf("read comments: %d", code)
	}
	entry, ok := f.handler.chat.applications.Lookup("douyin")
	if !ok {
		t.Fatal("douyin not registered")
	}
	provider := &localDouyinWebInteraction{home: f.handler.home, installation: entry.InstallationID}
	access, err := provider.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	draft := apphost.InteractionDraft{Target: provider.ref(access), ContentID: target, CommentID: "105", Kind: "reply_comment", Text: "合成回复"}
	draft.CommentVersion, err = provider.ResolveInteractionSubject(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint json.RawMessage
	clicks, queries := 0, 0
	manager.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
		if in.Submitted {
			if in.BeforeSubmit != nil {
				t.Fatal("receipt lookup retained a submit action")
			}
			queries++
			return stationbrowser.DouyinInteractionResult{Code: "receipt_not_found"}, stationbrowser.ErrDouyinInteractionUnknown
		}
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		if err := authorize(); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		clicks++
		return stationbrowser.DouyinInteractionResult{Submitted: true, Code: "platform_rate_limited"}, stationbrowser.ErrDouyinInteractionUnknown
	}
	request := apphost.InteractionRequest{Reference: apphost.InteractionReference{TaskID: "first", ManifestSHA256: "manifest", Target: draft.Target},
		Draft: draft, CheckActive: func(context.Context) error { return nil },
		SaveCheckpoint: func(_ context.Context, value json.RawMessage) error { checkpoint = value; return nil },
	}
	first, err := provider.PerformInteraction(t.Context(), request)
	if err != nil || first.Code != "platform_rate_limited" || first.NextAttemptAt == "" || clicks != 1 {
		t.Fatalf("first reply: %+v %v clicks=%d", first, err, clicks)
	}
	second := request
	second.Reference.TaskID = "second"
	held, err := provider.PerformInteraction(t.Context(), second)
	if err != nil || held.Code != "reply_cooldown" || held.NextAttemptAt != first.NextAttemptAt || clicks != 1 {
		t.Fatalf("second reply: %+v %v clicks=%d", held, err, clicks)
	}
	request.Checkpoint = checkpoint
	recovered, err := provider.PerformInteraction(t.Context(), request)
	if err != nil || recovered.Code != "receipt_not_found" || clicks != 1 || queries != 1 {
		t.Fatalf("resume: %+v %v clicks=%d queries=%d", recovered, err, clicks, queries)
	}
}
