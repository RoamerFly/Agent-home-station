package bot

import (
	"context"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type unavailableShareAnalyzer struct{}

func TestPublicShareTransportFailureCanRetryButBrowserChallengeCannot(t *testing.T) {
	if !retryableObservationReason("share_public_read_failed") {
		t.Fatal("transient public read lost recovery")
	}
	if retryableObservationReason("share_requires_browser") {
		t.Fatal("browser requirement must not spin HTTP retries")
	}
}

func (unavailableShareAnalyzer) Analyze(context.Context, linuxnative.Message) (string, error) {
	return "", &ObservationUnavailableError{Observation: "未能读取视频，需要浏览器", Reason: "shared_video_share_requires_browser"}
}
func TestFailedSharedContentReachesAgentForRecovery(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{true: "background", false: "sync"}[background], func(t *testing.T) {
			calls := 0
			w, c, n := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
				calls++
				if !strings.Contains(turns[len(turns)-1]["content"], "需要浏览器") {
					t.Error("failure not given to Agent")
				}
				return `{"action":"silent","messages":[]}`, nil
			}))
			if background {
				w.enableGenerations()
				defer w.closeGenerations()
			}
			w.config.Analyzer = unavailableShareAnalyzer{}
			m := fixtureMessage("share", "filehelper", "", "incoming")
			m.Type = 49
			w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
			w.handle(context.Background(), fixtureEvent("1", m.ID, m.ThreadID, "incoming", "remote"))
			if background {
				pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
			}
			scope := c.Store.Context(companion.Input{Channel: "wechat", Account: "test", Thread: m.ThreadID}, c.Config, time.Now())
			found := false
			for _, turn := range scope.Messages {
				found = found || turn.TraceID == m.ID && strings.Contains(turn.Text, "需要浏览器")
			}
			if !found || calls != 1 || len(n.submissions) != 0 {
				t.Fatal("failure was not delivered to model", found, calls)
			}
		})
	}
}

func TestShareRuntimeRecoveryRetriesBeforeReturning(t *testing.T) {
	m := fixtureMessage("share-retry", "retry@chatroom", "", "incoming")
	m.Type = 49
	calls, retries := 0, 0
	text, err := readMediaWithRecoveryHook(t.Context(), m, func(context.Context, linuxnative.Message) (string, error) {
		calls++
		if calls < 3 {
			return "", &ObservationUnavailableError{Reason: "share_plugin_runtime_unavailable"}
		}
		return "已读取分享内容", nil
	}, func(int, string, time.Time) { retries++ })
	if err != nil || text != "已读取分享内容" || calls != 3 || retries != 2 {
		t.Fatalf("runtime recovery did not retry: text=%q err=%v calls=%d retries=%d", text, err, calls, retries)
	}
}

func TestShareRuntimeFailureSchedulesDurableRetry(t *testing.T) {
	w, _, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		return `{"action":"silent","messages":[]}`, nil
	}))
	room := "retry-durable@chatroom"
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "share-durable", room, "incoming", "remote"), Phase: "processing", Updated: time.Now()}}
	reports := 0
	w.config.ObservationFailed = func(companion.Input, string) error { reports++; return nil }
	w.stopUnreadableTurn(companion.Input{Thread: room, TraceID: "share-durable", IsGroup: true}, &groupObservationFailure{reason: "share_plugin_runtime_unavailable", messageID: "share-durable"})
	item := w.runtime.Inbox[0]
	if item.Phase != "waiting_tool_retry" || item.ToolRetryAttempts != 1 || !item.RetryAfter.After(time.Now()) {
		t.Fatalf("runtime failure was not persisted for retry: %+v", item)
	}
	if reports != 0 {
		t.Fatal("temporary read failure was reported as a final group failure")
	}
}
