package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinRoundsRelationOnlyProbesSavedDetailCandidate(t *testing.T) {
	f, m, view := syntheticExecutionRoundFixture(t, true)
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatal(err)
	}
	const workID, noticeID = "7391234567890123456", "100"
	creatorUID := "900"
	digest := sha256.Sum256([]byte(creatorUID))
	creatorAccount := hex.EncodeToString(digest[:])
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.captureExecutionRounds(t.Context(), identity.FishID, identity.LocalOwner.ID, record,
		stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: creatorAccount,
			Items: []stationbrowser.DouyinNotice{{ID: noticeID, Type: 7, WorkID: workID,
				Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "cid", ID: "999"}}}},
			Details: []stationbrowser.DouyinNoticeDetail{{NoticeID: noticeID, NoticeType: 7, WorkID: workID,
				CandidateFieldsValid: true, Candidates: []stationbrowser.DouyinNoticeCandidate{
					{Path: "comment.cid", ID: "300"}, {Path: "comment.reply_id", ID: "400"},
					{Path: "comment.comment.cid", ID: "300"},
					{Path: "comment.user.uid", ID: "901"},
				}}}}); err != nil {
		t.Fatal(err)
	}
	readCalls := 0
	m.readRelation = func(_ context.Context, _ string, _ stationbrowser.DouyinCommentRelationRequest, _ func() error) (stationbrowser.DouyinCommentRelation, error) {
		readCalls++
		return stationbrowser.DouyinCommentRelation{}, nil
	}
	call := func(tools *localToolSet, args map[string]any) (map[string]any, error) {
		t.Helper()
		args["action"], args["date"], args["notice_id"] = "relation", executionRoundDay(), noticeID
		raw, _ := json.Marshal(args)
		value, err := tools.Call(t.Context(), localDouyinRoundsToolName, raw)
		if err != nil {
			return nil, err
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(value), &result); err != nil {
			t.Fatal(err)
		}
		return result, nil
	}
	args := func() map[string]any {
		return map[string]any{"target_path": "comment.cid", "target_id": "300",
			"root_path": "comment.reply_id", "root_id": "400"}
	}
	tools := noticeToolSet(t, f, "relation-before-grant", true)
	if result, err := call(tools, args()); err == nil || result != nil || readCalls != 0 {
		t.Fatalf("missing works/comments grant reached browser: %v %v reads=%d", result, err, readCalls)
	}
	update := func(action string, fields map[string]any) {
		t.Helper()
		fields["action"], fields["revision"] = action, view["revision"]
		code, result := f.request("POST", localDouyinWebRoute, fields, true)
		if code != 200 {
			t.Fatalf("%s grant: %d %v", action, code, result)
		}
		view = result
	}
	update("set_works", map[string]any{"allow_works": true})
	update("set_read", map[string]any{"allow_read": true, "read_scope": "home_and_videos", "allow_comments": true})
	update("set_memory", map[string]any{"memory_scope": douyinMemoryWorks, "allow_memory": true})
	update("set_memory", map[string]any{"memory_scope": douyinMemoryComments, "allow_memory": true})
	tools = noticeToolSet(t, f, "relation-before-owner", true)
	if result, err := call(tools, args()); err == nil || result != nil || readCalls != 0 {
		t.Fatalf("unverified creator work reached reader: %v %v reads=%d", result, err, readCalls)
	}
	record, _, err = m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	observation := &localDouyinWebObservation{home: m.home}
	access := observation.access(identity.FishID, identity.LocalOwner.ID, record, m.view(record))
	if err := observation.admitCreatorAccount(t.Context(), access, creatorAccount); err != nil {
		t.Fatal(err)
	}
	tools = noticeToolSet(t, f, "relation-before-work", true)
	if result, err := call(tools, args()); err == nil || result != nil || readCalls != 0 {
		t.Fatalf("missing own work reached reader: %v %v reads=%d", result, err, readCalls)
	}
	work := stationbrowser.DouyinCreatorWork{ID: workID, Title: "合成作品", Caption: "合成简介", Visibility: "公开", Video: true}
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, creatorAccount, []localDouyinMemoryRecord{{
		ID: workID, Work: workID, Label: work.Title, Text: creatorWorkText(work), Coverage: "合成本人作品",
	}}); err != nil {
		t.Fatal(err)
	}
	tools = noticeToolSet(t, f, "relation-allowed", true)
	for _, bad := range []map[string]any{
		{"target_path": "user.uid", "target_id": "901"},
		{"target_path": "cid", "target_id": "999"}, // list candidate is not detail proof
		{"target_path": "comment.cid", "target_id": "301"},
		{"target_path": "reply_comment.cid", "target_id": "300"},
		{"target_path": "comment.cid", "target_id": "300", "root_path": "comment.reply_id", "root_id": "401"},
		{"target_path": "comment.cid", "target_id": "300", "root_path": "comment.reply_id"},
	} {
		if result, err := call(tools, bad); err == nil || result != nil || readCalls != 0 {
			t.Fatalf("unbound detail candidate reached reader: %v %v reads=%d", result, err, readCalls)
		}
	}
	m.readRelation = func(_ context.Context, profile string, req stationbrowser.DouyinCommentRelationRequest,
		check func() error) (stationbrowser.DouyinCommentRelation, error) {
		readCalls++
		if !strings.Contains(profile, "web-login") || req.WorkID != workID || check() != nil {
			t.Fatalf("reader lost exact scope or authorization: profile=%q req=%+v", profile, req)
		}
		switch req {
		case stationbrowser.DouyinCommentRelationRequest{WorkID: workID, TargetID: "300"},
			stationbrowser.DouyinCommentRelationRequest{WorkID: workID, RootID: "400", TargetID: "300"}:
			return stationbrowser.DouyinCommentRelation{CID: "300", WorkID: workID, ReplyID: "400",
				ReplyToReplyID: "500", AuthorUID: "901", Text: "合成目标评论"}, nil
		case stationbrowser.DouyinCommentRelationRequest{WorkID: workID, TargetID: "400"}:
			return stationbrowser.DouyinCommentRelation{CID: "400", WorkID: workID, ReplyID: "0",
				ReplyToReplyID: "0", AuthorUID: "901", Text: "合成主评论"}, nil
		case stationbrowser.DouyinCommentRelationRequest{WorkID: workID, TargetID: "500"},
			stationbrowser.DouyinCommentRelationRequest{WorkID: workID, RootID: "400", TargetID: "500"}:
			return stationbrowser.DouyinCommentRelation{CID: "500", WorkID: workID, ReplyID: "400",
				ReplyToReplyID: "0", AuthorUID: creatorUID, Text: "合成本人子评论"}, nil
		default:
			t.Fatalf("reader was asked to scan an unexpected comment: %+v", req)
			return stationbrowser.DouyinCommentRelation{}, nil
		}
	}
	result, err := call(tools, args())
	if err != nil || result["state"] != "observed" || result["cid"] != "300" ||
		result["reply_id"] != "400" || result["reply_to_reply_id"] != "500" ||
		result["author_is_creator"] != false || result["text"] != "合成目标评论" ||
		result["root_state"] != "observed" || result["parent_state"] != "observed" ||
		result["parent_author_is_creator"] != true || result["thread_state"] != "self_child_observed" ||
		result["reply_eligible"] != false || readCalls != 3 {
		t.Fatalf("target relation became reply proof or lost fields: %v %v reads=%d", result, err, readCalls)
	}
	// The observed type-31 notice shape supplies a target CID but no root ID.
	// A child is classifiable only when that exact CID appears in the natural
	// main-list preview, which supplies the root and direct-parent IDs.
	derived, err := call(tools, map[string]any{"target_path": "comment.comment.cid", "target_id": "300"})
	if err != nil || derived["thread_state"] != "self_child_observed" || derived["reply_id"] != "400" ||
		derived["root_id"] != nil || derived["parent_author_is_creator"] != true || derived["reply_eligible"] != false {
		t.Fatalf("notice CID alone could not derive a bounded child relation: %v %v", derived, err)
	}
	selfChildReader := m.readRelation
	m.readRelation = func(ctx context.Context, profile string, req stationbrowser.DouyinCommentRelationRequest,
		check func() error) (stationbrowser.DouyinCommentRelation, error) {
		if req.RootID == "" && (req.TargetID == "300" || req.TargetID == "500") {
			return stationbrowser.DouyinCommentRelation{}, errors.Join(stationbrowser.ErrDouyinCommentTargetPending, check())
		}
		return selfChildReader(ctx, profile, req, check)
	}
	fallback, err := call(tools, args())
	if err != nil || fallback["thread_state"] != "self_child_observed" || fallback["reply_eligible"] != false {
		t.Fatalf("missing embedded reply did not fall back to exact reply list: %v %v", fallback, err)
	}
	m.readRelation = func(ctx context.Context, profile string, req stationbrowser.DouyinCommentRelationRequest,
		check func() error) (stationbrowser.DouyinCommentRelation, error) {
		if req.TargetID == "500" {
			return stationbrowser.DouyinCommentRelation{CID: "500", WorkID: workID, ReplyID: "400",
				ReplyToReplyID: "0", AuthorUID: "902", Text: "他人的子评论"}, check()
		}
		return selfChildReader(ctx, profile, req, check)
	}
	otherChild, err := call(tools, args())
	if err != nil || otherChild["thread_state"] != "other_child_observed" ||
		otherChild["parent_author_is_creator"] != false || otherChild["reply_eligible"] != false {
		t.Fatalf("another person's child was not excluded: %v %v", otherChild, err)
	}
	m.readRelation = func(ctx context.Context, profile string, req stationbrowser.DouyinCommentRelationRequest,
		check func() error) (stationbrowser.DouyinCommentRelation, error) {
		if req.TargetID == "300" {
			return stationbrowser.DouyinCommentRelation{CID: "300", WorkID: workID, ReplyID: "400",
				ReplyToReplyID: "0", AuthorUID: "901", Text: "直接回复主评论"}, check()
		}
		t.Fatalf("direct-root child caused extra relation probe: %+v", req)
		return stationbrowser.DouyinCommentRelation{}, nil
	}
	directRoot, err := call(tools, args())
	if err != nil || directRoot["thread_state"] != "direct_parent_is_root" || directRoot["reply_eligible"] != false {
		t.Fatalf("direct reply to root was not excluded: %v %v", directRoot, err)
	}
	m.readRelation = func(ctx context.Context, profile string, req stationbrowser.DouyinCommentRelationRequest,
		check func() error) (stationbrowser.DouyinCommentRelation, error) {
		if req.TargetID == "400" {
			return stationbrowser.DouyinCommentRelation{}, errors.Join(stationbrowser.ErrDouyinCommentTargetPending, check())
		}
		return selfChildReader(ctx, profile, req, check)
	}
	pendingRoot, err := call(tools, args())
	if err != nil || pendingRoot["root_state"] != "pending" || pendingRoot["parent_state"] != nil ||
		pendingRoot["thread_state"] != "unverified" || pendingRoot["reply_eligible"] != false {
		t.Fatalf("missing natural root page became a reply proof: %v %v", pendingRoot, err)
	}
	m.readRelation = selfChildReader
	if err := tools.ValidateGenerationSources(t.Context()); err != nil {
		t.Fatalf("relation source was not retained: %v", err)
	}
	commentSource, granted := douyinMemorySource(record, identity.FishID, identity.LocalOwner.ID, douyinMemoryComments)
	if !granted || len(tools.douyinRoundProofs) == 0 ||
		tools.douyinRoundProofs[len(tools.douyinRoundProofs)-1].CommentsSource != commentSource {
		t.Fatal("observed comment body was not bound to its comments-memory grant")
	}
	for _, failure := range []struct {
		err           error
		state, reason string
	}{
		{stationbrowser.ErrDouyinLoginActive, "busy", "browser_profile_in_use"},
		{stationbrowser.ErrDouyinCommentTargetPending, "pending", "target_not_in_natural_page"},
	} {
		m.readRelation = func(_ context.Context, _ string, _ stationbrowser.DouyinCommentRelationRequest, check func() error) (stationbrowser.DouyinCommentRelation, error) {
			readCalls++
			return stationbrowser.DouyinCommentRelation{}, errors.Join(failure.err, check())
		}
		result, err := call(tools, args())
		if err != nil || result["state"] != failure.state || result["stop_reason"] != failure.reason ||
			result["reply_eligible"] != false || result["cid"] != nil {
			t.Fatalf("busy/pending became action or vanished: %v %v", result, err)
		}
	}
	for _, bad := range []stationbrowser.DouyinCommentRelation{
		{CID: "300", WorkID: "7391234567890123457", ReplyID: "400", ReplyToReplyID: "500", AuthorUID: "901", Text: "错误作品"},
		{CID: "301", WorkID: workID, ReplyID: "400", ReplyToReplyID: "500", AuthorUID: "901", Text: "错误目标"},
		{CID: "300", WorkID: workID, ReplyID: "401", ReplyToReplyID: "500", AuthorUID: "901", Text: "错误主楼"},
	} {
		m.readRelation = func(_ context.Context, _ string, _ stationbrowser.DouyinCommentRelationRequest, check func() error) (stationbrowser.DouyinCommentRelation, error) {
			if err := check(); err != nil {
				return stationbrowser.DouyinCommentRelation{}, err
			}
			return bad, nil
		}
		if result, err := call(tools, args()); err == nil || result != nil {
			t.Fatalf("reader returned another work, target or root: %v %v", result, err)
		}
	}
	m.readRelation = func(_ context.Context, _ string, _ stationbrowser.DouyinCommentRelationRequest, check func() error) (stationbrowser.DouyinCommentRelation, error) {
		if err := check(); err != nil {
			return stationbrowser.DouyinCommentRelation{}, err
		}
		update("set_memory", map[string]any{"memory_scope": douyinMemoryComments, "allow_memory": false})
		return stationbrowser.DouyinCommentRelation{}, stationbrowser.ErrDouyinCommentTargetPending
	}
	if result, err := call(tools, args()); err == nil || result != nil {
		t.Fatalf("revoked comment grant returned a pending relation: %v %v", result, err)
	}
	if err := tools.ValidateGenerationSources(t.Context()); err == nil {
		t.Fatal("comment grant revoked after a successful relation remained valid for final generation")
	}
}
