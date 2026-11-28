package station

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinExecutionWorkContextRequiresSameCreatorAndRetainedProof(t *testing.T) {
	f, m, view := syntheticExecutionRoundFixture(t, false)
	const workID = "7391234567890123456"
	account := strings.Repeat("c", 64)
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatal(err)
	}
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.captureExecutionRounds(t.Context(), identity.FishID, identity.LocalOwner.ID, record,
		stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: account,
			Items: []stationbrowser.DouyinNotice{{ID: "context-1", WorkID: workID}}}); err != nil {
		t.Fatal(err)
	}
	url := localDouyinExecutionContextPath + "?date=" + executionRoundDay() + "&work_id=" + workID
	if code, result := f.request("GET", url, nil, false); code != http.StatusOK || result["ownership"] != "unverified" || result["creator_text"] != nil || result["reply_eligible"] != false {
		t.Fatalf("without works memory: %d %v", code, result)
	}
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "revision": view["revision"],
		"memory_scope": douyinMemoryWorks, "allow_memory": true}, true)
	if code != http.StatusOK {
		t.Fatalf("works memory grant: %d %v", code, view)
	}
	record, _, err = m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	observation := &localDouyinWebObservation{home: m.home}
	access := observation.access(identity.FishID, identity.LocalOwner.ID, record, m.view(record))
	if err := observation.admitCreatorAccount(t.Context(), access, account); err != nil {
		t.Fatal(err)
	}
	work := stationbrowser.DouyinCreatorWork{ID: workID, Title: "合成海边作品", Caption: "傍晚海浪与天空", Visibility: "公开", Video: true}
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, account, []localDouyinMemoryRecord{{
		ID: workID, Work: workID, Label: work.Title, Text: creatorWorkText(work),
		Coverage: "本人作品 · 创作者管理页文字快照", At: "2026-11-26T00:00:00.000000000Z",
	}}); err != nil {
		t.Fatal(err)
	}
	code, result := f.request("GET", url, nil, false)
	if code != http.StatusOK || result["ownership"] != "verified_own_work" ||
		!strings.Contains(result["creator_text"].(string), "简介 傍晚海浪与天空") || result["audio_complete"] != false || result["visual_complete"] != false ||
		result["media_page_count"] != float64(0) || len(result["media_pages"].([]any)) != 0 || result["reply_eligible"] != false {
		t.Fatalf("verified description only: %d %v", code, result)
	}
	// Successful existing observation pages, never fetched for this test, are
	// retained with exact source and logical window/frame coverage.
	target := localDouyinWebTarget{videoID: workID, creatorAccount: account}
	hash := strings.Repeat("a", 64)
	for _, page := range []apphost.ObservationPage{
		{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: hash, StartSeconds: 0, WindowEndSeconds: 5, MediaDurationSeconds: 10},
			Chunks: []apphost.ObservationChunk{{Text: "海浪声中的开场白"}}, Coverage: "音轨前半段"},
		{AudioProof: &apphost.AudioProof{State: "no_recognizable_speech", MediaSHA256: hash, StartSeconds: 5, WindowEndSeconds: 10, MediaDurationSeconds: 10},
			Coverage: "音轨后半段", Complete: true},
	} {
		if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationAudio, page); err != nil {
			t.Fatal(err)
		}
	}
	for _, page := range []apphost.ObservationPage{
		{VisualProof: &apphost.VisualFrameProof{MediaSHA256: hash, StartFrame: 0, NextFrame: 2, FrameCount: 4, FrameStep: 1, MediaDurationSeconds: 10,
			FrameIndexes: []int{0, 1}, TimestampSeconds: []float64{0, 2}}, Chunks: []apphost.ObservationChunk{{Text: "开场采样帧见海面"}}, Coverage: "采样帧 0–1"},
		{VisualProof: &apphost.VisualFrameProof{MediaSHA256: hash, StartFrame: 2, NextFrame: 4, FrameCount: 4, FrameStep: 1, MediaDurationSeconds: 10,
			FrameIndexes: []int{2, 3}, TimestampSeconds: []float64{5, 8}}, Chunks: []apphost.ObservationChunk{{Text: "结尾采样帧见落日"}}, Coverage: "采样帧 2–3", Complete: true},
	} {
		if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationVisual, page); err != nil {
			t.Fatal(err)
		}
	}
	savedVisual, err := m.readSavedWorkVisual(t.Context(), record, identity.FishID, identity.LocalOwner.ID, account, work)
	if err != nil || savedVisual.VideoSummary != "" {
		t.Fatalf("video visual exposed without visual grant: %+v %v", savedVisual, err)
	}
	memoryDB, err := openLocalDouyinMemory(t.Context(), m.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer memoryDB.Close()
	source, granted := douyinMemorySource(record, identity.FishID, identity.LocalOwner.ID, douyinMemoryWorks)
	if !granted {
		t.Fatal("works memory grant missing")
	}
	savedVisual = localDouyinSavedWorkVisual{}
	err = readSavedDouyinVideoVisual(t.Context(), memoryDB, source, workID, &savedVisual)
	if err != nil || !savedVisual.VideoComplete || savedVisual.VideoPages != 2 || savedVisual.VideoSHA256 != hash ||
		!strings.Contains(savedVisual.VideoSummary, "开场采样帧见海面") || !strings.Contains(savedVisual.VideoSummary, "结尾采样帧见落日") {
		t.Fatalf("saved video visual: %+v %v", savedVisual, err)
	}
	code, result = f.request("GET", url+"&limit=2", nil, false)
	if code != http.StatusOK || result["audio_complete"] != true || result["visual_complete"] != true ||
		result["media_page_count"] != float64(4) || len(result["media_pages"].([]any)) != 2 || result["next_offset"] != float64(2) ||
		result["snapshot_state"] != "stable" || result["restart_required"] != false || result["next_cursor"] == "" {
		t.Fatalf("retained media evidence page: %d %v", code, result)
	}
	firstCursor := result["next_cursor"].(string)
	for _, other := range []string{
		localDouyinExecutionContextPath + "?date=" + executionRoundDay() + "&work_id=7391234567890123457&cursor=" + firstCursor,
		localDouyinExecutionContextPath + "?date=2026-11-25&work_id=" + workID + "&cursor=" + firstCursor,
	} {
		code, result = f.request("GET", other, nil, false)
		if code != http.StatusOK || result["restart_required"] != true || result["creator_text"] != nil || len(result["media_pages"].([]any)) != 0 {
			t.Fatalf("context cursor crossed work or date: %d %v", code, result)
		}
	}
	code, result = f.request("GET", url+"&limit=2&cursor="+firstCursor, nil, false)
	if code != http.StatusOK || len(result["media_pages"].([]any)) != 2 || result["next_offset"] != nil ||
		result["next_cursor"] != "" || result["snapshot_state"] != "stable" {
		t.Fatalf("pinned media evidence continuation: %d %v", code, result)
	}
	code, result = f.request("GET", url+"&limit=2&offset=2", nil, false)
	if code != http.StatusOK || len(result["media_pages"].([]any)) != 2 || result["next_offset"] != nil ||
		result["snapshot_state"] != "legacy_offset_unpinned" || result["next_cursor"] != "" {
		t.Fatalf("retained media evidence continuation: %d %v", code, result)
	}
	if code, _ := f.request("GET", url+"&cursor=invalid", nil, false); code != http.StatusBadRequest {
		t.Fatalf("malformed context cursor: %d", code)
	}
	if code, _ := f.request("GET", url+"&cursor="+firstCursor+"&offset=2", nil, false); code != http.StatusBadRequest {
		t.Fatalf("context cursor with offset: %d", code)
	}
	// Adding a page under the same media SHA must invalidate a prior offset
	// even though the chosen media original has not changed.
	if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationAudio,
		apphost.ObservationPage{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: hash,
			StartSeconds: 10, WindowEndSeconds: 12, MediaDurationSeconds: 12},
			Chunks: []apphost.ObservationChunk{{Text: "后补的音轨页"}}, Coverage: "后补音轨页"}); err != nil {
		t.Fatal(err)
	}
	code, result = f.request("GET", url+"&limit=2&cursor="+firstCursor, nil, false)
	if code != http.StatusOK || result["restart_required"] != true || len(result["media_pages"].([]any)) != 0 {
		t.Fatalf("inserted media page reused old cursor: %d %v", code, result)
	}
	code, result = f.request("GET", url+"&limit=2", nil, false)
	if code != http.StatusOK || result["next_cursor"] == "" || result["media_page_count"] != float64(5) {
		t.Fatalf("new media page could not be reread from start: %d %v", code, result)
	}
	insertCursor := result["next_cursor"].(string)
	if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationAudio,
		apphost.ObservationPage{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: hash,
			StartSeconds: 10, WindowEndSeconds: 12, MediaDurationSeconds: 12},
			Chunks: []apphost.ObservationChunk{{Text: "更新后的补读音轨页"}}, Coverage: "更新后的补读页"}); err != nil {
		t.Fatal(err)
	}
	code, result = f.request("GET", url+"&limit=2&cursor="+insertCursor, nil, false)
	if code != http.StatusOK || result["restart_required"] != true || len(result["media_pages"].([]any)) != 0 {
		t.Fatalf("updated same-SHA media page reused old cursor: %d %v", code, result)
	}
	code, result = f.request("GET", url+"&limit=2", nil, false)
	if code != http.StatusOK || result["next_cursor"] == "" {
		t.Fatalf("updated media page could not be reread from start: %d %v", code, result)
	}
	updatedCursor := result["next_cursor"].(string)
	newHash := strings.Repeat("b", 64)
	if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationAudio,
		apphost.ObservationPage{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: newHash,
			StartSeconds: 0, WindowEndSeconds: 5, MediaDurationSeconds: 10},
			Chunks: []apphost.ObservationChunk{{Text: "新原件仅处理了开头"}}, Coverage: "新原件音轨前半段"}); err != nil {
		t.Fatal(err)
	}
	code, result = f.request("GET", url+"&limit=2&cursor="+updatedCursor, nil, false)
	if code != http.StatusOK || result["restart_required"] != true || result["snapshot_state"] != "restart_required" ||
		result["ownership"] != "unverified" || len(result["media_pages"].([]any)) != 0 || result["creator_text"] != nil || result["next_cursor"] != "" {
		t.Fatalf("changed media reused old page cursor: %d %v", code, result)
	}
	code, result = f.request("GET", url, nil, false)
	if code != http.StatusOK || result["media_sha256"] != newHash || result["media_page_count"] != float64(1) ||
		result["audio_complete"] != false || result["visual_complete"] != false || result["snapshot_state"] != "stable" {
		t.Fatalf("new partial original inherited old completeness: %d %v", code, result)
	}
	// An additional notification for the same work but another account removes
	// all contextual disclosure; a work ID alone is never ownership proof.
	if err := m.captureExecutionRounds(context.Background(), identity.FishID, identity.LocalOwner.ID, record,
		stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: strings.Repeat("d", 64),
			Items: []stationbrowser.DouyinNotice{{ID: "context-2", WorkID: workID}}}); err != nil {
		t.Fatal(err)
	}
	code, result = f.request("GET", url, nil, false)
	if code != http.StatusOK || result["ownership"] != "unverified" || result["creator_text"] != nil || len(result["media_pages"].([]any)) != 0 {
		t.Fatalf("cross-account group leaked context: %d %v", code, result)
	}
}

func TestCompleteLocalDouyinWorkMediaRejectsGapAndInconsistentProof(t *testing.T) {
	hash := strings.Repeat("a", 64)
	a0 := localDouyinStoredWorkMedia{startAudio: 0, endAudio: 5,
		page: localDouyinWorkContextPage{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: hash,
			StartSeconds: 0, WindowEndSeconds: 5, MediaDurationSeconds: 10}}}
	a1 := localDouyinStoredWorkMedia{startAudio: 5, endAudio: 10,
		page: localDouyinWorkContextPage{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: hash,
			StartSeconds: 5, WindowEndSeconds: 10, MediaDurationSeconds: 10}, Complete: true}}
	v0 := localDouyinStoredWorkMedia{startFrame: 0, nextFrame: 2,
		page: localDouyinWorkContextPage{VisualProof: &apphost.VisualFrameProof{MediaSHA256: hash,
			StartFrame: 0, NextFrame: 2, FrameCount: 4, FrameStep: 1, MediaDurationSeconds: 10,
			FrameIndexes: []int{0, 1}, TimestampSeconds: []float64{0, 2}}}}
	v1 := localDouyinStoredWorkMedia{startFrame: 2, nextFrame: 4,
		page: localDouyinWorkContextPage{VisualProof: &apphost.VisualFrameProof{MediaSHA256: hash,
			StartFrame: 2, NextFrame: 4, FrameCount: 4, FrameStep: 1, MediaDurationSeconds: 10,
			FrameIndexes: []int{2, 3}, TimestampSeconds: []float64{5, 8}}, Complete: true}}
	if audio, visual := completeLocalDouyinWorkMedia([]localDouyinStoredWorkMedia{a0, a1, v0, v1}); !audio || !visual {
		t.Fatal("valid synthetic page chain should be complete")
	}
	if audio, visual := completeLocalDouyinWorkMedia([]localDouyinStoredWorkMedia{a0, v0, v1}); audio || !visual {
		t.Fatal("missing audio window should remain incomplete")
	}
	changedAudio := a1
	proof := *a1.page.AudioProof
	proof.MediaDurationSeconds = 11
	changedAudio.page.AudioProof = &proof
	if audio, _ := completeLocalDouyinWorkMedia([]localDouyinStoredWorkMedia{a0, changedAudio}); audio {
		t.Fatal("inconsistent audio duration completed")
	}
	changedVisual := v1
	frameProof := *v1.page.VisualProof
	frameProof.FrameStep = 2
	changedVisual.page.VisualProof = &frameProof
	if _, visual := completeLocalDouyinWorkMedia([]localDouyinStoredWorkMedia{v0, changedVisual}); visual {
		t.Fatal("inconsistent visual sampling step completed")
	}
	changedTerminal := a1
	terminalProof := *a1.page.AudioProof
	terminalProof.WindowEndSeconds = 9
	changedTerminal.page.AudioProof = &terminalProof
	changedTerminal.endAudio = 9
	if audio, _ := completeLocalDouyinWorkMedia([]localDouyinStoredWorkMedia{a0, changedTerminal}); audio {
		t.Fatal("premature audio terminal completed")
	}
}
