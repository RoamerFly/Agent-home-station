package station

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

// This synthetic provider exercises Observer checkpoint semantics without a
// browser, video download, ASR service, visual model, or real account.
type douyinRoundPrepareStub struct {
	t                 *testing.T
	handle, wrong     string
	workID, hash      string
	observation       *localDouyinWebObservation
	access            localDouyinWebAccess
	target            localDouyinWebTarget
	pageTextReads     int
	audioReads        int
	visualReads       int
	audioSecondError  error
	visualSecondError error
}

func (s *douyinRoundPrepareStub) Connection(context.Context) (apphost.Connection, bool, error) {
	return apphost.Connection{ID: douyinapp.WebReadConnectionID, AccountID: "synthetic-creator", Configuration: "ready"}, true, nil
}

func (s *douyinRoundPrepareStub) Targets(context.Context) ([]apphost.ObservationTarget, error) {
	return nil, nil
}

func (s *douyinRoundPrepareStub) Grant(_ context.Context, access apphost.ObservationAccess) (apphost.ObservationGrant, error) {
	if access.ContentID != s.handle && access.ContentID != s.wrong {
		return apphost.ObservationGrant{Allowed: false}, nil
	}
	return apphost.ObservationGrant{Allowed: true, Version: "synthetic-prepare-v1",
		Fresh:            access.Field == apphost.ObservationPageText,
		RefreshCompleted: access.Field == apphost.ObservationAudio || access.Field == apphost.ObservationVisual}, nil
}

func (s *douyinRoundPrepareStub) Read(ctx context.Context, request apphost.ObservationRequest) (apphost.ObservationPage, error) {
	if request.ContentID == s.wrong {
		s.pageTextReads++
		return apphost.ObservationPage{Complete: true, Coverage: "合成错误作品入口",
			Chunks: []apphost.ObservationChunk{{Text: "其他作品", SourceID: "7391234567890123457",
				SourceURL: stationbrowser.DouyinCreatorManageURL}}}, nil
	}
	if request.ContentID != s.handle {
		s.t.Fatalf("unexpected content ID: %q", request.ContentID)
	}
	var page apphost.ObservationPage
	switch request.Field {
	case apphost.ObservationPageText:
		s.pageTextReads++
		page = apphost.ObservationPage{Complete: true, Coverage: "合成本人作品入口",
			Chunks: []apphost.ObservationChunk{{Text: "合成作品", SourceID: s.workID,
				SourceURL: stationbrowser.DouyinCreatorManageURL}}}
	case apphost.ObservationAudio:
		s.audioReads++
		if request.Cursor != "" && s.audioSecondError != nil {
			return apphost.ObservationPage{}, s.audioSecondError
		}
		start := 0.0
		if request.Cursor != "" {
			if request.Cursor != "audio-next" {
				s.t.Fatalf("unexpected audio cursor: %q", request.Cursor)
			}
			start = 60
		}
		end := min(100, start+float64(request.Limit))
		page = apphost.ObservationPage{Coverage: "合成音轨", Complete: end == 100,
			Chunks: []apphost.ObservationChunk{{Text: "合成音轨文本", SourceID: s.hash, Position: int(start)}},
			AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: s.hash,
				RequestedSeconds: request.Limit, StartSeconds: start, WindowEndSeconds: end,
				DecodedSeconds: end - start, MediaDurationSeconds: 100}}
		if !page.Complete {
			page.NextCursor = "audio-next"
		}
	case apphost.ObservationVisual:
		s.visualReads++
		if request.Cursor != "" && s.visualSecondError != nil {
			return apphost.ObservationPage{}, s.visualSecondError
		}
		start := 0
		if request.Cursor != "" {
			if request.Cursor != "visual-next" {
				s.t.Fatalf("unexpected visual cursor: %q", request.Cursor)
			}
			start = 1
		}
		page = apphost.ObservationPage{Coverage: "合成采样画面", Complete: start == 1,
			Chunks: []apphost.ObservationChunk{{Text: "合成画面文本", SourceID: s.hash,
				SourceURL: s.target.url, Position: start}},
			VisualProof: &apphost.VisualFrameProof{MediaSHA256: s.hash,
				RequestedFrames: request.Limit, StartFrame: start, NextFrame: start + 1,
				FrameCount: 2, FrameStep: 1, MediaDurationSeconds: 100,
				FrameIndexes: []int{start}, TimestampSeconds: []float64{float64(start)}}}
		if !page.Complete {
			page.NextCursor = "visual-next"
		}
	default:
		s.t.Fatalf("unexpected observation field: %q", request.Field)
	}
	if request.Field == apphost.ObservationAudio || request.Field == apphost.ObservationVisual {
		if err := s.observation.captureCreatorMediaContext(ctx, s.access, s.target, request.Field, page); err != nil {
			return apphost.ObservationPage{}, err
		}
	}
	return page, nil
}

func TestLocalDouyinRoundsPrepareResumesOneWorkAndSkipsCompleteMedia(t *testing.T) {
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
			Items: []stationbrowser.DouyinNotice{{ID: "prepare-notice-1", WorkID: workID}}}); err != nil {
		t.Fatal(err)
	}
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "revision": view["revision"],
		"memory_scope": douyinMemoryWorks, "allow_memory": true}, true)
	if code != 200 {
		t.Fatalf("works memory grant: %d %v", code, view)
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
	work := stationbrowser.DouyinCreatorWork{ID: workID, Title: "合成作品", Caption: "合成简介", Visibility: "公开", Video: true}
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, creator, []localDouyinMemoryRecord{{
		ID: workID, Work: workID, Label: work.Title, Text: creatorWorkText(work), Coverage: "本人作品 · 合成快照",
	}}); err != nil {
		t.Fatal(err)
	}
	stub := &douyinRoundPrepareStub{t: t, handle: douyinapp.WebCreatorWorkPrefix + strings.Repeat("1", 32),
		wrong: douyinapp.WebCreatorWorkPrefix + strings.Repeat("2", 32), workID: workID,
		hash: strings.Repeat("a", 64), observation: observation, access: access,
		target: localDouyinWebTarget{url: "https://www.douyin.com/video/" + workID,
			videoID: workID, creatorAccount: creator}}
	registry, err := apphost.New(douyinapp.NewWebOnly(stub).Registration())
	if err != nil {
		t.Fatal(err)
	}
	tools := noticeToolSet(t, f, "prepare-synthetic", true)
	tools.observations = apphost.NewObserver(registry, t.TempDir())
	call := func(args map[string]any) (map[string]any, error) {
		t.Helper()
		args["action"], args["date"], args["work_id"] = "prepare", executionRoundDay(), workID
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
	if _, err := call(map[string]any{"content_id": stub.wrong}); !errors.Is(err, apphost.ErrObservationForbidden) ||
		stub.audioReads != 0 || stub.visualReads != 0 {
		t.Fatalf("wrong creator handle reached media: audio=%d visual=%d err=%v", stub.audioReads, stub.visualReads, err)
	}
	first, err := call(map[string]any{"content_id": stub.handle, "audio_pages": 1, "audio_seconds": 60})
	if err != nil || first["state"] != "pending" || first["audio_pages_read"] != float64(1) ||
		first["audio_complete"] != false || first["context_complete"] != false || first["can_resume"] != true ||
		!strings.Contains(first["resume_boundary"].(string), "20 分钟") {
		t.Fatalf("first audio page: %v %v", first, err)
	}
	stub.audioSecondError = apphost.ErrObservationWebSnapshotChanged
	restart, err := call(map[string]any{"content_id": stub.handle, "audio_pages": 1, "audio_seconds": 60})
	if err != nil || restart["state"] != "restart_required" || restart["can_resume"] != false ||
		restart["stop_reason"] != "creator_works_changed" {
		t.Fatalf("changed creator snapshot implied resumable cursor: %v %v", restart, err)
	}
	stub.audioSecondError = nil
	second, err := call(map[string]any{"content_id": stub.handle, "audio_pages": 1, "audio_seconds": 60})
	if err != nil || second["state"] != "requested_fields_complete" || second["audio_complete"] != true ||
		second["visual_complete"] != false || second["context_complete"] != false || second["can_resume"] != true ||
		second["stop_reason"] != "visual_incomplete_set_audio_pages_0_and_visual_pages_positive" {
		t.Fatalf("audio complete must request visual next: %v %v", second, err)
	}
	third, err := call(map[string]any{"content_id": stub.handle, "audio_pages": 0, "visual_pages": 1, "visual_frames": 1})
	if err != nil || third["state"] != "pending" || third["audio_pages_read"] != float64(0) ||
		third["visual_pages_read"] != float64(1) || third["context_complete"] != false || stub.audioReads != 3 {
		t.Fatalf("first visual page reprocessed audio: %v %v audio=%d", third, err, stub.audioReads)
	}
	stub.visualSecondError = apphost.ErrObservationVisualSourceChanged
	changed, err := call(map[string]any{"content_id": stub.handle, "audio_pages": 0, "visual_pages": 1, "visual_frames": 1})
	if err != nil || changed["state"] != "source_changed" || changed["can_resume"] != false ||
		changed["stop_reason"] != "media_sha_changed" {
		t.Fatalf("changed original implied resumable cursor: %v %v", changed, err)
	}
	stub.visualSecondError = nil
	fourth, err := call(map[string]any{"content_id": stub.handle, "audio_pages": 0, "visual_pages": 1, "visual_frames": 1})
	if err != nil || fourth["state"] != "requested_fields_complete" || fourth["context_complete"] != true ||
		fourth["media_sha256"] != stub.hash || stub.audioReads != 3 || stub.visualReads != 3 {
		t.Fatalf("same-media completion: %v %v audio=%d visual=%d", fourth, err, stub.audioReads, stub.visualReads)
	}
	final, err := call(map[string]any{"content_id": stub.handle, "audio_pages": 2, "visual_pages": 2})
	if err != nil || final["context_complete"] != true || final["audio_pages_read"] != float64(0) ||
		final["visual_pages_read"] != float64(0) || stub.audioReads != 3 || stub.visualReads != 3 {
		t.Fatalf("complete checkpoints were reprocessed: %v %v audio=%d visual=%d", final, err, stub.audioReads, stub.visualReads)
	}
	if stub.pageTextReads != 8 || stub.audioReads != 3 || stub.visualReads != 3 {
		t.Fatalf("unexpected provider callbacks: page=%d audio=%d visual=%d", stub.pageTextReads, stub.audioReads, stub.visualReads)
	}
	if err := tools.ValidateGenerationSources(t.Context()); err != nil {
		t.Fatalf("prepared source did not remain authorized: %v", err)
	}
}
