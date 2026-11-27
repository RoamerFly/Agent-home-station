package station

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestDouyinRoundContextReadStaysWithinOneWorkAndOneToolTurn(t *testing.T) {
	const day = "2026-11-27"
	const workA = "7391234567890123456"
	const workB = "7391234567890123457"
	creator := "作品编号 " + workA + "\n简介 合成简介"
	mediaSHA := strings.Repeat("a", 64)
	cursor := localDouyinExecutionContextCursor{Version: 1, Date: day, WorkID: workA, Offset: 1,
		Grant: strings.Repeat("b", 64), Epoch: "synthetic-epoch", MediaSHA256: mediaSHA,
		CreatorKey: localCheckpointKey("douyin-context-creator-v1", creator, "合成", "2026-11-27T00:00:00Z")}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	nextCursor := base64.RawURLEncoding.EncodeToString(encoded)
	newPage := func(work, text string, total int, next string) localDouyinExecutionWorkContext {
		return localDouyinExecutionWorkContext{WorkID: work, Ownership: "verified_own_work", CreatorText: text,
			CreatorCoverage: "合成", CreatorObservedAt: "2026-11-27T00:00:00Z", MediaSHA256: mediaSHA,
			AudioComplete: true, VisualComplete: true, MediaPages: []localDouyinWorkContextPage{{Text: "合成证据"}},
			MediaPageCount: total, NextCursor: next, SnapshotState: "stable",
			contextEpoch: cursor.Epoch, contextGrant: cursor.Grant}
	}
	tools := &localToolSet{}
	first := newPage(workA, creator, 2, nextCursor)
	tools.recordDouyinRoundContextRead(localDouyinRoundsInput{Date: day, WorkID: workA}, &first)
	if first.AgentContextReadComplete || first.AgentPagesRead != 1 {
		t.Fatalf("first page marked complete: %+v", first)
	}
	if len(tools.douyinRoundContextCompleted) != 0 {
		t.Fatal("first page was retained as complete")
	}
	other := newPage(workB, "作品编号 "+workB, 1, "")
	tools.recordDouyinRoundContextRead(localDouyinRoundsInput{Date: day, WorkID: workB}, &other)
	if !other.AgentContextReadComplete || other.AgentPagesRead != 1 {
		t.Fatalf("other work failed its independent one-page read: %+v", other)
	}
	otherSnapshot := tools.douyinRoundContextCompleted[douyinRoundContextReadKey(day, workB)]
	if otherSnapshot.Date != day || otherSnapshot.WorkID != workB || otherSnapshot.PagesRead != 1 ||
		otherSnapshot.Epoch != cursor.Epoch || otherSnapshot.Grant != cursor.Grant {
		t.Fatalf("single-page completion lost its private snapshot: %+v", otherSnapshot)
	}
	last := newPage(workA, creator, 2, "")
	tools.recordDouyinRoundContextRead(localDouyinRoundsInput{Date: day, WorkID: workA, Cursor: nextCursor}, &last)
	if !last.AgentContextReadComplete || last.AgentPagesRead != 2 {
		t.Fatalf("first work did not complete after exact continuation: %+v", last)
	}
	completed := tools.douyinRoundContextCompleted[douyinRoundContextReadKey(day, workA)]
	if completed.Date != day || completed.WorkID != workA || completed.PagesRead != 2 || completed.Total != 2 ||
		completed.MediaSHA != mediaSHA || completed.CreatorKey != cursor.CreatorKey ||
		completed.Epoch != cursor.Epoch || completed.Grant != cursor.Grant {
		t.Fatalf("completed read was not pinned to one work and snapshot: %+v", completed)
	}
	output, err := json.Marshal(last)
	if err != nil || strings.Contains(string(output), cursor.Epoch) || strings.Contains(string(output), cursor.Grant) {
		t.Fatalf("private snapshot fields leaked in context response: %s %v", output, err)
	}
	freshTurn := &localToolSet{}
	replayed := newPage(workA, creator, 2, "")
	freshTurn.recordDouyinRoundContextRead(localDouyinRoundsInput{Date: day, WorkID: workA, Cursor: nextCursor}, &replayed)
	if replayed.AgentContextReadComplete || replayed.AgentPagesRead != 0 {
		t.Fatalf("prior-turn cursor masqueraded as this turn's read: %+v", replayed)
	}
	if len(freshTurn.douyinRoundContextCompleted) != 0 {
		t.Fatal("prior-turn completion was reused")
	}
	legacy := newPage(workA, creator, 1, "")
	tools.recordDouyinRoundContextRead(localDouyinRoundsInput{Date: day, WorkID: workA, Offset: 1}, &legacy)
	if legacy.AgentContextReadComplete || len(tools.douyinRoundContextCompleted) != 1 {
		t.Fatalf("legacy offset retained a completed read: %+v %+v", legacy, tools.douyinRoundContextCompleted)
	}
	incomplete := newPage(workA, creator, 1, "")
	incomplete.VisualComplete = false
	tools.recordDouyinRoundContextRead(localDouyinRoundsInput{Date: day, WorkID: workA}, &incomplete)
	if incomplete.AgentContextReadComplete || len(tools.douyinRoundContextCompleted) != 1 {
		t.Fatalf("incomplete media gained a completed read: %+v %+v", incomplete, tools.douyinRoundContextCompleted)
	}
	restarted := newPage(workB, "作品编号 "+workB, 1, "")
	restarted.RestartRequired, restarted.SnapshotState = true, "restart_required"
	tools.recordDouyinRoundContextRead(localDouyinRoundsInput{Date: day, WorkID: workB}, &restarted)
	if restarted.AgentContextReadComplete || len(tools.douyinRoundContextCompleted) != 0 {
		t.Fatalf("restart retained completed evidence: %+v %+v", restarted, tools.douyinRoundContextCompleted)
	}
}
