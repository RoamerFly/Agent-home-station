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
