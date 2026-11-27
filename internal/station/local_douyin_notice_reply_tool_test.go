package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

// All identifiers and browser responses in this file are synthetic. The test
// exercises only job creation and rejection; it never submits an interaction.
func syntheticNoticeReplyToolFixture(t *testing.T) (*localHomeFixture, *localDouyinWebLogin, *localToolSet, string) {
	t.Helper()
	f, m, view := syntheticExecutionRoundFixture(t, false)
	const creatorUID = "900"
	digest := sha256.Sum256([]byte(creatorUID))
	creatorAccount := hex.EncodeToString(digest[:])
	// Bind the notification and creator accounts to the same synthetic UID.
	code, next := f.request("POST", localDouyinNoticePath, map[string]any{"action": "revoke", "revision": view["revision"]}, true)
	if code != 200 {
		t.Fatalf("revoke synthetic notice account: %d %v", code, next)
	}
	m.probeNotice = func(_ context.Context, _ string, check func() error) (string, error) {
		return creatorAccount, check()
	}
	code, view = f.request("POST", localDouyinNoticePath, map[string]any{"action": "bind", "revision": next["revision"]}, true)
	if code != 200 {
		t.Fatalf("bind synthetic creator account: %d %v", code, view)
	}
	view = setSyntheticNoticeAgent(t, f, true)
	update := func(action string, fields map[string]any) {
		t.Helper()
		fields["action"], fields["revision"] = action, view["revision"]
		var result map[string]any
		code, result = f.request("POST", localDouyinWebRoute, fields, true)
		if code != 200 {
			t.Fatalf("%s synthetic grant: %d %v", action, code, result)
		}
		view = result
	}
	update("set_works", map[string]any{"allow_works": true})
	update("set_read", map[string]any{"allow_read": true, "read_scope": "home_and_videos", "allow_comments": true})
	update("set_interactions", map[string]any{"allow_replies": true})
	update("set_memory", map[string]any{"memory_scope": douyinMemoryWorks, "allow_memory": true})
	update("set_memory", map[string]any{"memory_scope": douyinMemoryComments, "allow_memory": true})
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatalf("synthetic owner: %v", err)
	}
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	const workID = "7391234567890123456"
	page := stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: creatorAccount,
		Items: []stationbrowser.DouyinNotice{
			{ID: "101", Type: 31, WorkID: workID, Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "comment.comment.cid", ID: "300"}}},
			{ID: "102", Type: 31, WorkID: workID, Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "comment.comment.cid", ID: "301"}, {Path: "comment.parent_id", ID: "400"}}},
			{ID: "103", Type: 31, WorkID: workID, Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "comment.comment.cid", ID: "302"}, {Path: "comment.parent_id", ID: "400"}}},
			{ID: "104", Type: 31, WorkID: workID, Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "comment.comment.cid", ID: "303"}}},
		}}
	if err := m.captureExecutionRounds(t.Context(), identity.FishID, identity.LocalOwner.ID, record, page); err != nil {
		t.Fatalf("capture synthetic notice list: %v", err)
	}
	observation := &localDouyinWebObservation{home: m.home}
	access := observation.access(identity.FishID, identity.LocalOwner.ID, record, m.view(record))
	if err := observation.admitCreatorAccount(t.Context(), access, creatorAccount); err != nil {
		t.Fatalf("bind synthetic creator owner: %v", err)
	}
	work := stationbrowser.DouyinCreatorWork{ID: workID, Title: "合成作品", Caption: "合成简介", Visibility: "公开", Video: true}
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, creatorAccount, []localDouyinMemoryRecord{{
		ID: workID, Work: workID, Label: work.Title, Text: creatorWorkText(work), Coverage: "合成本人作品",
	}}); err != nil {
		t.Fatalf("capture synthetic work: %v", err)
	}
	mediaSHA := strings.Repeat("a", 64)
	target := localDouyinWebTarget{videoID: workID, creatorAccount: creatorAccount}
	if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationAudio,
		apphost.ObservationPage{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: mediaSHA,
			StartSeconds: 0, WindowEndSeconds: 10, MediaDurationSeconds: 10},
			Chunks: []apphost.ObservationChunk{{Text: "合成音轨"}}, Coverage: "音轨 0–10 秒", Complete: true}); err != nil {
		t.Fatalf("capture synthetic audio: %v", err)
	}
	if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationVisual,
		apphost.ObservationPage{VisualProof: &apphost.VisualFrameProof{MediaSHA256: mediaSHA, StartFrame: 0,
			NextFrame: 1, FrameCount: 1, FrameStep: 1, MediaDurationSeconds: 10,
			FrameIndexes: []int{0}, TimestampSeconds: []float64{0}},
			Chunks: []apphost.ObservationChunk{{Text: "合成画面"}}, Coverage: "采样帧 0", Complete: true}); err != nil {
		t.Fatalf("capture synthetic visual: %v", err)
	}
	contentID := douyinapp.WebCreatorWorkPrefix + strings.Repeat("b", 32)
	if err := os.MkdirAll(observation.snapshotDirectory(access), 0700); err != nil {
		t.Fatalf("prepare synthetic snapshot directory: %v", err)
	}
	if err := observation.saveCreatorCommentTarget(access, contentID, target); err != nil {
		t.Fatalf("save synthetic creator work link: %v", err)
	}
	m.readRelation = func(_ context.Context, _ string, req stationbrowser.DouyinCommentRelationRequest,
		check func() error) (stationbrowser.DouyinCommentRelation, error) {
		if req.WorkID != workID || check() != nil {
			t.Fatalf("relation escaped synthetic work or authorization: %+v", req)
		}
		switch req.TargetID {
		case "300", "303":
			return stationbrowser.DouyinCommentRelation{CID: req.TargetID, WorkID: workID, ReplyID: "0",
				ReplyToReplyID: "0", AuthorUID: "901", Text: "合成主评论"}, nil
		case "301":
			return stationbrowser.DouyinCommentRelation{CID: "301", WorkID: workID, ReplyID: "400",
				ReplyToReplyID: "500", AuthorUID: "901", Text: "合成孙评论"}, nil
		case "302":
			return stationbrowser.DouyinCommentRelation{CID: "302", WorkID: workID, ReplyID: "400",
				ReplyToReplyID: "0", AuthorUID: "901", Text: "合成直接回复主楼"}, nil
		case "400":
			return stationbrowser.DouyinCommentRelation{CID: "400", WorkID: workID, ReplyID: "0",
				ReplyToReplyID: "0", AuthorUID: "901", Text: "合成主楼"}, nil
		case "500":
			return stationbrowser.DouyinCommentRelation{CID: "500", WorkID: workID, ReplyID: "400",
				ReplyToReplyID: "0", AuthorUID: creatorUID, Text: "合成本人子评论"}, nil
		default:
			t.Fatalf("unexpected relation probe: %+v", req)
			return stationbrowser.DouyinCommentRelation{}, nil
		}
	}
	tools := noticeToolSet(t, f, "notice-reply-synthetic", true)
	raw, _ := json.Marshal(map[string]any{"action": "context", "date": executionRoundDay(), "work_id": workID, "limit": 20})
	value, err := tools.Call(t.Context(), localDouyinRoundsToolName, raw)
	if err != nil {
		t.Fatalf("read synthetic work context: %v", err)
	}
	var contextView map[string]any
	if err := json.Unmarshal([]byte(value), &contextView); err != nil || contextView["agent_context_read_complete"] != true {
		t.Fatalf("synthetic work context incomplete: %v %v", contextView, err)
	}
	return f, m, tools, contentID
}

func callSyntheticNoticeReply(t *testing.T, tools *localToolSet, noticeID, targetID, contentID string, root bool) (map[string]any, error) {
	t.Helper()
	args := map[string]any{"action": "create", "date": executionRoundDay(), "notice_id": noticeID,
		"target_path": "comment.comment.cid", "target_id": targetID, "content_id": contentID,
		"text": "合成回复正文"}
	if root {
		args["root_path"], args["root_id"] = "comment.parent_id", "400"
	}
	raw, _ := json.Marshal(args)
	value, err := tools.Call(t.Context(), localDouyinNoticeReplyToolName, raw)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func TestLocalDouyinNoticeReplyCreatesOnlyBoundedPendingJobs(t *testing.T) {
	_, _, tools, contentID := syntheticNoticeReplyToolFixture(t)
	listed, err := tools.List(t.Context())
	if err != nil || !hasTool(listed, localDouyinNoticeReplyToolName) {
		t.Fatalf("notice reply tool absent: %v %v", listed, err)
	}
	for _, tc := range []struct {
		name, notice, target string
		root                 bool
	}{
		{"top_level", "101", "300", false},
		{"self_child", "102", "301", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, err := callSyntheticNoticeReply(t, tools, tc.notice, tc.target, contentID, tc.root)
			if err != nil || job["job_id"] == "" || job["state"] != "pending" || job["comment_id"] != tc.target {
				t.Fatalf("bounded reply did not create pending job: %v %v", job, err)
			}
		})
	}
	if job, err := callSyntheticNoticeReply(t, tools, "103", "302", contentID, true); err == nil || job != nil {
		t.Fatalf("direct reply to root created a job: %v %v", job, err)
	}
}

func TestLocalDouyinNoticeReplyRejectsConflictedCandidateAndIncompleteContext(t *testing.T) {
	f, m, tools, contentID := syntheticNoticeReplyToolFixture(t)
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatal(err)
	}
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.captureExecutionRounds(t.Context(), identity.FishID, identity.LocalOwner.ID, record,
		stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: record.NoticeAccount,
			Items: []stationbrowser.DouyinNotice{{ID: "104", Type: 31, WorkID: "7391234567890123456",
				Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "comment.comment.cid", ID: "304"}}}}}); err != nil {
		t.Fatal(err)
	}
	for _, targetID := range []string{"303", "304"} {
		if job, err := callSyntheticNoticeReply(t, tools, "104", targetID, contentID, false); err == nil || job != nil {
			t.Fatalf("conflicted candidate %s created a job: %v %v", targetID, job, err)
		}
	}
	// A fresh turn has no complete Agent context read, even though the same
	// private work snapshot remains available to the host.
	fresh := noticeToolSet(t, f, "notice-reply-incomplete-context", true)
	if job, err := callSyntheticNoticeReply(t, fresh, "101", "300", contentID, false); err == nil || job != nil {
		t.Fatalf("fresh turn reused completed context: %v %v", job, err)
	}
}
