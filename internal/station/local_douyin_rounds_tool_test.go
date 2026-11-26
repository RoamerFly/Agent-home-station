package station

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/workspacefs"
)

func TestLocalDouyinRoundsToolReadsSavedWorkContextWithoutPlatformAccess(t *testing.T) {
	f, m, view := syntheticExecutionRoundFixture(t, false)
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatal(err)
	}
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	const workID = "7391234567890123456"
	creator := strings.Repeat("c", 64)
	if err := m.captureExecutionRounds(t.Context(), identity.FishID, identity.LocalOwner.ID, record,
		stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: creator,
			Items: []stationbrowser.DouyinNotice{{ID: "round-tool-1", WorkID: workID}, {ID: "round-tool-2", WorkID: workID}, {ID: "round-tool-3", WorkID: ""}}}); err != nil {
		t.Fatal(err)
	}
	platformReads := 0
	m.readNotice = func(context.Context, string, stationbrowser.DouyinNoticeRequest, func() error) (stationbrowser.DouyinNoticePage, error) {
		platformReads++
		return stationbrowser.DouyinNoticePage{}, nil
	}
	call := func(tools *localToolSet, args map[string]any) (map[string]any, error) {
		t.Helper()
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
	day := executionRoundDay()
	denied := noticeToolSet(t, f, "round-tool-denied", true)
	if result, err := call(denied, map[string]any{"action": "works", "date": day}); err == nil || result != nil {
		t.Fatalf("notice binding alone exposed Agent ledger: %v %v", result, err)
	}
	view = setSyntheticNoticeAgent(t, f, true)
	tools := noticeToolSet(t, f, "round-tool-allowed", true)
	listed, err := tools.List(t.Context())
	if err != nil || !hasTool(listed, localDouyinRoundsToolName) {
		t.Fatalf("read-only rounds tool absent: %v %v", listed, err)
	}
	first, err := call(tools, map[string]any{"action": "works", "date": day, "limit": 1})
	if err != nil || len(first["groups"].([]any)) != 1 || first["next_cursor"] == "" {
		t.Fatalf("work group first page: %v %v", first, err)
	}
	second, err := call(tools, map[string]any{"action": "works", "date": day, "limit": 1, "cursor": first["next_cursor"]})
	if err != nil || len(second["groups"].([]any)) != 1 || second["groups"].([]any)[0].(map[string]any)["work_id"] == first["groups"].([]any)[0].(map[string]any)["work_id"] {
		t.Fatalf("work group continuation: %v %v", second, err)
	}
	page, err := call(tools, map[string]any{"action": "notices", "date": day, "work_id": workID, "limit": 1})
	if err != nil || len(page["rounds"].([]any)) != 1 || page["next_before"] == "" || page["rounds"].([]any)[0].(map[string]any)["reply_eligible"] != false {
		t.Fatalf("notice first page: %v %v", page, err)
	}
	next, err := call(tools, map[string]any{"action": "notices", "date": day, "work_id": workID, "limit": 1, "before": page["next_before"]})
	if err != nil || len(next["rounds"].([]any)) != 1 || next["rounds"].([]any)[0].(map[string]any)["work_id"] != workID {
		t.Fatalf("notice continuation escaped work: %v %v", next, err)
	}
	unverified, err := call(tools, map[string]any{"action": "context", "date": day, "work_id": workID})
	if err != nil || unverified["ownership"] != "unverified" || unverified["creator_text"] != nil {
		t.Fatalf("unverified work disclosed context: %v %v", unverified, err)
	}
	if _, err := call(tools, map[string]any{"action": "context", "date": day, "work_id": workID, "cursor": "wrong-field"}); err == nil {
		t.Fatal("context accepted a work-group cursor")
	}
	code, current := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "revision": view["revision"],
		"memory_scope": douyinMemoryWorks, "allow_memory": true}, true)
	if code != 200 {
		t.Fatalf("works memory grant: %d %v", code, current)
	}
	record, _, err = m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	observation := &localDouyinWebObservation{home: m.home}
	access := observation.access(identity.FishID, identity.LocalOwner.ID, record, m.view(record))
	if err := observation.admitCreatorAccount(t.Context(), access, creator); err != nil {
		t.Fatal(err)
	}
	work := stationbrowser.DouyinCreatorWork{ID: workID, Title: "合成作品", Caption: "合成的简介", Visibility: "公开", Video: true}
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, creator, []localDouyinMemoryRecord{{
		ID: workID, Work: workID, Label: work.Title, Text: creatorWorkText(work), Coverage: "本人作品 · 合成快照",
	}}); err != nil {
		t.Fatal(err)
	}
	tools = noticeToolSet(t, f, "round-tool-context", true)
	descriptionOnly, err := call(tools, map[string]any{"action": "context", "date": day, "work_id": workID})
	if err != nil || descriptionOnly["ownership"] != "verified_own_work" || descriptionOnly["audio_complete"] != false ||
		descriptionOnly["visual_complete"] != false || descriptionOnly["media_page_count"] != float64(0) {
		t.Fatalf("creator description before media observation: %v %v", descriptionOnly, err)
	}
	hash := strings.Repeat("a", 64)
	target := localDouyinWebTarget{videoID: workID, creatorAccount: creator}
	if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationAudio,
		apphost.ObservationPage{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: hash, StartSeconds: 0,
			WindowEndSeconds: 10, MediaDurationSeconds: 10}, Chunks: []apphost.ObservationChunk{{Text: "合成音轨"}}, Coverage: "音轨 0–10 秒", Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationVisual,
		apphost.ObservationPage{VisualProof: &apphost.VisualFrameProof{MediaSHA256: hash, StartFrame: 0, NextFrame: 1,
			FrameCount: 1, FrameStep: 1, MediaDurationSeconds: 10, FrameIndexes: []int{0}, TimestampSeconds: []float64{0}},
			Chunks: []apphost.ObservationChunk{{Text: "合成画面"}}, Coverage: "采样帧 0", Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := tools.ValidateGenerationSources(t.Context()); err != nil {
		t.Fatalf("adding observed media pages blocked same-turn continuation: %v", err)
	}
	contextPage, err := call(tools, map[string]any{"action": "context", "date": day, "work_id": workID, "limit": 1})
	if err != nil || contextPage["ownership"] != "verified_own_work" || contextPage["audio_complete"] != true ||
		contextPage["visual_complete"] != true || !strings.Contains(contextPage["creator_text"].(string), "简介 合成的简介") || contextPage["next_offset"] != float64(1) {
		t.Fatalf("saved context first page: %v %v", contextPage, err)
	}
	contextNext, err := call(tools, map[string]any{"action": "context", "date": day, "work_id": workID, "limit": 1, "offset": 1})
	if err != nil || len(contextNext["media_pages"].([]any)) != 1 || contextNext["next_offset"] != nil {
		t.Fatalf("saved context continuation: %v %v", contextNext, err)
	}
	if platformReads != 0 {
		t.Fatalf("stored ledger tool read platform %d times", platformReads)
	}
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, creator, []localDouyinMemoryRecord{{
		ID: workID, Work: workID, Label: "更新后标题", Text: "作品编号 " + workID + "\n标题 更新后标题",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := tools.ValidateGenerationSources(t.Context()); err != nil {
		t.Fatalf("ordinary works-memory update blocked same-turn continuation: %v", err)
	}
	updated, err := call(tools, map[string]any{"action": "context", "date": day, "work_id": workID})
	if err != nil || !strings.Contains(updated["creator_text"].(string), "更新后标题") {
		t.Fatalf("context could not be refreshed after a same-work update: %v %v", updated, err)
	}
	code, current = f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "revision": current["revision"],
		"memory_scope": douyinMemoryWorks, "allow_memory": false}, true)
	if code != 200 {
		t.Fatalf("revoke works memory: %d %v", code, current)
	}
	if err := tools.ValidateGenerationSources(t.Context()); err == nil {
		t.Fatal("revoked works-memory grant left previously read context usable")
	}
}

func TestLocalDouyinRoundsLocateOnlySavedNoticeWorkAndResumesIndependentScan(t *testing.T) {
	f, m, view := syntheticExecutionRoundFixture(t, true)
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatal(err)
	}
	const workID = "7391234567890123456"
	creator := strings.Repeat("c", 64)
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.captureExecutionRounds(t.Context(), identity.FishID, identity.LocalOwner.ID, record,
		stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: creator,
			Items: []stationbrowser.DouyinNotice{{ID: "locate-1", WorkID: workID}}}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	m.readWorks = func(_ context.Context, _ string, in stationbrowser.DouyinCreatorWorksRequest, authorize func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		reads++
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCreatorWorksPage{}, err
		}
		if in.Page != 0 {
			t.Fatalf("unexpected creator page: %+v", in)
		}
		return stationbrowser.DouyinCreatorWorksPage{Account: creator, PageKey: "synthetic-page", CoverageKnown: true,
			Works: []stationbrowser.DouyinCreatorWork{
				{ID: "7391234567890123457", Title: "其他作品", Visibility: "公开", Video: true},
				{ID: workID, Title: "目标作品", Caption: "目标简介", Visibility: "公开", Video: true},
			}}, nil
	}
	tools := noticeToolSet(t, f, "locate-denied", true)
	call := func(tools *localToolSet, input map[string]any) (map[string]any, error) {
		t.Helper()
		raw, _ := json.Marshal(input)
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
	day := executionRoundDay()
	if _, err := call(tools, map[string]any{"action": "locate", "date": day, "work_id": workID}); err == nil || reads != 0 {
		t.Fatalf("works memory denial reached browser: reads=%d err=%v", reads, err)
	}
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "revision": view["revision"],
		"memory_scope": douyinMemoryWorks, "allow_memory": true}, true)
	if code != 200 {
		t.Fatalf("works memory grant: %d %v", code, view)
	}
	tools = noticeToolSet(t, f, "locate-allowed", true)
	if _, err := call(tools, map[string]any{"action": "locate", "date": day, "work_id": workID, "limit": 1}); err == nil {
		t.Fatal("locate silently accepted unrelated limit")
	}
	if _, err := call(tools, map[string]any{"action": "locate", "date": day, "work_id": "7391234567890123458"}); err == nil || reads != 0 {
		t.Fatalf("unsaved work triggered creator scan: reads=%d err=%v", reads, err)
	}
	first, err := call(tools, map[string]any{"action": "locate", "date": day, "work_id": workID, "pages": 1, "page_size": 1})
	if err != nil || first["state"] != "pending" || first["can_resume"] != true || first["pages_read"] != float64(1) ||
		first["works_scanned"] != float64(1) || first["content_id"] != nil {
		t.Fatalf("first bounded scan: %v %v", first, err)
	}
	second, err := call(tools, map[string]any{"action": "locate", "date": day, "work_id": workID, "pages": 1, "page_size": 1, "resume": true})
	if err != nil || second["state"] != "found" || second["ownership"] != "verified_own_work" ||
		second["resumed"] != true || second["can_resume"] != false ||
		!strings.HasPrefix(second["content_id"].(string), douyinapp.WebCreatorWorkPrefix) || reads != 2 {
		t.Fatalf("target continuation: %v %v reads=%d", second, err, reads)
	}
	fields := second["fields"].([]any)
	if len(fields) == 0 || fields[0] != string(apphost.ObservationPageText) || second["reply_eligible"] != false {
		t.Fatalf("unexpected locate fields: %v", second)
	}
	if err := tools.ValidateGenerationSources(t.Context()); err != nil {
		t.Fatalf("found source proof changed: %v", err)
	}
	completed, err := call(tools, map[string]any{"action": "locate", "date": day, "work_id": workID, "page_size": 1, "resume": true})
	if err != nil || completed["state"] != "refresh_required" || completed["content_id"] != nil || reads != 2 {
		t.Fatalf("cached completed scan implied absence or leaked stale handle: %v %v reads=%d", completed, err, reads)
	}
	record, _, err = m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(stationObservationsDir(m.home.dataDir), "douyin-round-locate",
		localCheckpointKey(localDouyinExecutionScope(identity.FishID, identity.LocalOwner.ID, record), day, workID))
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	release, err := hostfile.TryLock(filepath.Join(dir, "scan.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := call(tools, map[string]any{"action": "locate", "date": day, "work_id": workID, "resume": true}); !errors.Is(err, apphost.ErrObservationWebSessionBusy) {
		t.Fatalf("concurrent scan was not fenced: %v", err)
	}
}
