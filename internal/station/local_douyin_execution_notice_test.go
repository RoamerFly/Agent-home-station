package station

import (
	"context"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/stationbrowser"
)

func TestReadExecutionNoticeKeepsExactScopeAndCandidatesNonActionable(t *testing.T) {
	_, m, _ := syntheticExecutionRoundFixture(t, false)
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatal(err)
	}
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	platformReads := 0
	m.readNotice = func(context.Context, string, stationbrowser.DouyinNoticeRequest, func() error) (stationbrowser.DouyinNoticePage, error) {
		platformReads++
		return stationbrowser.DouyinNoticePage{}, nil
	}
	creator := strings.Repeat("c", 64)
	page := stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: creator,
		Items: []stationbrowser.DouyinNotice{
			{ID: "context-1", Type: 7, WorkID: "200", Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "cid", ID: "399"}}},
			{ID: "context-2", Type: 7, WorkID: "300"},
		},
		Details: []stationbrowser.DouyinNoticeDetail{{NoticeID: "context-1", NoticeType: 7, WorkID: "200",
			CandidateFieldsValid: true, Candidates: []stationbrowser.DouyinNoticeCandidate{{Path: "comment.cid", ID: "400"}}}},
		DetailProgress: "partial"}
	if err := m.captureExecutionRounds(t.Context(), identity.FishID, identity.LocalOwner.ID, record, page); err != nil {
		t.Fatal(err)
	}
	scope := localDouyinExecutionScope(identity.FishID, identity.LocalOwner.ID, record)
	got, found, err := m.readExecutionNotice(t.Context(), scope, "context-1")
	if err != nil || !found {
		t.Fatalf("read one notice: found=%v err=%v", found, err)
	}
	observedAt, err := time.Parse(time.RFC3339Nano, got.round.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.round.NoticeID != "context-1" || got.round.WorkID != "200" || got.round.Type != 7 ||
		got.round.DetailStatus != "observed_candidate" || len(got.round.Candidates) != 1 ||
		got.round.Candidates[0].ID != "399" || len(got.round.DetailCandidates) != 1 ||
		got.round.DetailCandidates[0].ID != "400" || got.round.ReplyEligible ||
		got.creatorAccount != creator || got.observedDate != observedAt.In(localDouyinExecutionZone).Format("2006-01-02") {
		t.Fatalf("saved notice evidence changed or became actionable: %+v", got)
	}
	pending, found, err := m.readExecutionNotice(t.Context(), scope, "context-2")
	if err != nil || !found || pending.round.DetailStatus != "partial" || pending.round.WorkID != "300" ||
		len(pending.round.DetailCandidates) != 0 || pending.round.ReplyEligible {
		t.Fatalf("pending notice lost its boundary: %+v found=%v err=%v", pending, found, err)
	}
	for _, lookup := range []struct{ scope, id string }{
		{scope, "missing"},
		{localCheckpointKey("other-scope", scope), "context-1"},
		{scope, ""},
	} {
		if other, found, err := m.readExecutionNotice(t.Context(), lookup.scope, lookup.id); err != nil || found || other.round.NoticeID != "" {
			t.Fatalf("missing/cross-scope notice exposed: %+v found=%v err=%v", other, found, err)
		}
	}
	if platformReads != 0 {
		t.Fatalf("ledger lookup touched platform: %d", platformReads)
	}
}

func TestLocalDouyinNoticeReplyRequestKeyBindsAccountAndNotice(t *testing.T) {
	fish := "fish-" + strings.Repeat("a", 48)
	creator := strings.Repeat("c", 64)
	key, ok := localDouyinNoticeReplyRequestKey(fish, "owner-one", creator, "context-1")
	if !ok || len(key) != 64 {
		t.Fatalf("stable notice key unavailable: %q %v", key, ok)
	}
	if again, ok := localDouyinNoticeReplyRequestKey(fish, "owner-one", creator, "context-1"); !ok || again != key {
		t.Fatalf("same notice did not retain durable key: %q %v", again, ok)
	}
	for _, input := range [][4]string{
		{"fish-" + strings.Repeat("b", 48), "owner-one", creator, "context-1"},
		{fish, "owner-two", creator, "context-1"},
		{fish, "owner-one", strings.Repeat("d", 64), "context-1"},
		{fish, "owner-one", creator, "context-2"},
	} {
		if other, ok := localDouyinNoticeReplyRequestKey(input[0], input[1], input[2], input[3]); !ok || other == key {
			t.Fatalf("distinct notice identity reused key: %q %v", other, ok)
		}
	}
	for _, input := range [][4]string{
		{"invalid", "owner-one", creator, "context-1"},
		{fish, "", creator, "context-1"},
		{fish, "owner-one", "not-a-digest", "context-1"},
		{fish, "owner-one", creator, ""},
		{fish, "owner-one", creator, "line\nbreak"},
	} {
		if key, ok := localDouyinNoticeReplyRequestKey(input[0], input[1], input[2], input[3]); ok || key != "" {
			t.Fatalf("invalid notice identity made key: %q %v", key, ok)
		}
	}
}
