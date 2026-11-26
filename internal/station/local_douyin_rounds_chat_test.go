package station

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/stationbrowser"
)

func TestDouyinRoundContextIsReadPerWorkThroughAgentChat(t *testing.T) {
	f, m, view := syntheticExecutionRoundFixture(t, true)
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatal(err)
	}
	const workA, workB = "7391234567890123456", "7391234567890123457"
	creator := strings.Repeat("c", 64)
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.captureExecutionRounds(t.Context(), identity.FishID, identity.LocalOwner.ID, record,
		stationbrowser.DouyinNoticePage{Account: record.NoticeAccount, CreatorAccount: creator,
			Items: []stationbrowser.DouyinNotice{{ID: "chat-round-a", WorkID: workA}, {ID: "chat-round-b", WorkID: workB}}}); err != nil {
		t.Fatal(err)
	}
	code, _ := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "revision": view["revision"],
		"memory_scope": douyinMemoryWorks, "allow_memory": true}, true)
	if code != 200 {
		t.Fatal("synthetic works-memory grant failed")
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
	for i, workID := range []string{workA, workB} {
		work := stationbrowser.DouyinCreatorWork{ID: workID, Title: fmt.Sprintf("合成作品 %d", i+1),
			Caption: fmt.Sprintf("只属于作品 %d 的简介", i+1), Visibility: "公开", Video: true}
		if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, creator, []localDouyinMemoryRecord{{
			ID: workID, Work: workID, Label: work.Title, Text: creatorWorkText(work), Coverage: "合成本人作品",
		}}); err != nil {
			t.Fatal(err)
		}
		mediaSHA := strings.Repeat([]string{"a", "b"}[i], 64)
		target := localDouyinWebTarget{videoID: workID, creatorAccount: creator}
		if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationAudio,
			apphost.ObservationPage{AudioProof: &apphost.AudioProof{State: "transcribed", MediaSHA256: mediaSHA,
				StartSeconds: 0, WindowEndSeconds: 10, MediaDurationSeconds: 10},
				Chunks:   []apphost.ObservationChunk{{Text: fmt.Sprintf("作品 %d 的合成音轨", i+1)}},
				Coverage: "音轨 0–10 秒", Complete: true}); err != nil {
			t.Fatal(err)
		}
		if err := observation.captureCreatorMediaContext(t.Context(), access, target, apphost.ObservationVisual,
			apphost.ObservationPage{VisualProof: &apphost.VisualFrameProof{MediaSHA256: mediaSHA,
				StartFrame: 0, NextFrame: 1, FrameCount: 1, FrameStep: 1,
				MediaDurationSeconds: 10, FrameIndexes: []int{0}, TimestampSeconds: []float64{0}},
				Chunks:   []apphost.ObservationChunk{{Text: fmt.Sprintf("作品 %d 的合成画面", i+1)}},
				Coverage: "采样帧 0", Complete: true}); err != nil {
			t.Fatal(err)
		}
	}
	platformReads := 0
	m.readNotice = func(context.Context, string, stationbrowser.DouyinNoticeRequest, func() error) (stationbrowser.DouyinNoticePage, error) {
		platformReads++
		return stationbrowser.DouyinNoticePage{}, nil
	}
	day := executionRoundDay()
	var cursorA, cursorB string
	calls := 0
	lastRoundResult := func(turns []map[string]string) map[string]any {
		t.Helper()
		for i := len(turns) - 1; i >= 0; i-- {
			var envelope struct{ Tool, Result string }
			if json.Unmarshal([]byte(turns[i]["content"]), &envelope) != nil || envelope.Tool != localDouyinRoundsToolName {
				continue
			}
			var result map[string]any
			if json.Unmarshal([]byte(envelope.Result), &result) != nil {
				t.Fatal("Agent did not receive a structured rounds result")
			}
			return result
		}
		t.Fatal("Agent did not receive a rounds result")
		return nil
	}
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			calls++
			switch calls {
			case 1:
				return localToolCall(localDouyinRoundsToolName, map[string]any{
					"action": "context", "date": day, "work_id": workA, "limit": 1}), nil
			case 2, 3, 4, 5:
				result := lastRoundResult(turns)
				expectedWork := []string{"", "", workA, workB, workA, workB}[calls]
				expectedIndex := 1
				if expectedWork == workB {
					expectedIndex = 2
				}
				expectedPages := float64(1)
				if calls >= 4 {
					expectedPages = 2
				}
				if result["work_id"] != expectedWork || result["audio_complete"] != true ||
					result["visual_complete"] != true || result["agent_pages_read"] != expectedPages ||
					result["agent_context_read_complete"] != (calls >= 4) {
					t.Fatalf("Agent received a mixed or incomplete work context at call %d: %v", calls, result)
				}
				creatorText, _ := result["creator_text"].(string)
				pages, _ := result["media_pages"].([]any)
				if !strings.Contains(creatorText, fmt.Sprintf("只属于作品 %d 的简介", expectedIndex)) || len(pages) != 1 ||
					!strings.Contains(pages[0].(map[string]any)["text"].(string), fmt.Sprintf("作品 %d 的合成", expectedIndex)) {
					t.Fatalf("Agent received another work's caption or media at call %d: %v", calls, result)
				}
				switch calls {
				case 2:
					cursorA, _ = result["next_cursor"].(string)
					if cursorA == "" {
						t.Fatal("first work has no continuation cursor")
					}
					return localToolCall(localDouyinRoundsToolName, map[string]any{
						"action": "context", "date": day, "work_id": workB, "limit": 1}), nil
				case 3:
					cursorB, _ = result["next_cursor"].(string)
					if cursorB == "" || cursorB == cursorA {
						t.Fatal("second work cursor was absent or mixed with the first")
					}
					return localToolCall(localDouyinRoundsToolName, map[string]any{
						"action": "context", "date": day, "work_id": workA, "limit": 1, "cursor": cursorA}), nil
				case 4:
					return localToolCall(localDouyinRoundsToolName, map[string]any{
						"action": "context", "date": day, "work_id": workB, "limit": 1, "cursor": cursorB}), nil
				default:
					return `{"action":"reply","messages":["两个作品的已存音画证据已分别读完。"]}`, nil
				}
			default:
				t.Fatal("Agent made an unexpected extra model call")
				return "", nil
			}
		}), nil
	}
	if code, response := localChatSend(f, "synthetic-douyin-context-chat", "分别核对两件作品的已存音画证据"); code != 200 || response["status"] != "completed" || calls != 5 || platformReads != 0 {
		t.Fatalf("Agent context chat did not finish from stored evidence: HTTP=%d response=%v calls=%d platform_reads=%d",
			code, response, calls, platformReads)
	}
	calls = 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			calls++
			if calls == 1 {
				return localToolCall(localDouyinRoundsToolName, map[string]any{
					"action": "context", "date": day, "work_id": workA, "limit": 1, "cursor": cursorA}), nil
			}
			result := lastRoundResult(turns)
			if result["agent_context_read_complete"] != false || result["agent_pages_read"] != nil {
				t.Fatalf("another chat reused the prior turn's page credit: %v", result)
			}
			return `{"action":"reply","messages":["旧游标不能证明本回合已读完整作品。"]}`, nil
		}), nil
	}
	if code, response := localChatSend(f, "synthetic-douyin-context-next-chat", "检查旧游标能否代表本回合阅读"); code != 200 || response["status"] != "completed" || calls != 2 || platformReads != 0 {
		t.Fatalf("old context cursor crossed chat turns: HTTP=%d response=%v calls=%d platform_reads=%d",
			code, response, calls, platformReads)
	}
}
