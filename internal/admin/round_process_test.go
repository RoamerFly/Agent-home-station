package admin

import (
	"net/http"
	"strings"
	"testing"

	"wechat-bot/internal/bot"
)

func TestManualRoundActionsDoNotClaimSuccessWithoutRunningActor(t *testing.T) {
	f := setup(t)
	f.login()
	const thread, message = "room@chatroom", "old-message"
	f.manager.record(bot.Record{Event: "message_received", Category: "received", ThreadID: thread, MessageID: message})
	request := map[string]any{"thread_id": thread, "message_id": message}
	if code, _, _ := f.request("POST", "/api/logs/round/process", request, true, false); code != http.StatusForbidden {
		t.Fatal("manual process bypassed CSRF", code)
	}
	if code, body, _ := f.request("POST", "/api/logs/round/process", request, true, true); code != http.StatusConflict || !strings.Contains(string(body), "尚未触发") {
		t.Fatal("manual process claimed to run without the actor", code, string(body))
	}
	if code, body, _ := f.request("POST", "/api/logs/round/close", request, true, true); code != http.StatusConflict || !strings.Contains(string(body), "未生效") {
		t.Fatal("round close claimed to stop an unavailable actor", code, string(body))
	}
}

func TestManualProcessFindsClosedRoundInArchivedActivity(t *testing.T) {
	f := setup(t)
	entry := LogEntry{Event: "round_manually_closed", ThreadID: "room@chatroom", MessageID: "old-message"}
	if _, err := f.manager.historyArchive().Put("activity", activityScope(), []LogEntry{entry}); err != nil {
		t.Fatal(err)
	}
	s := &Server{manager: f.manager}
	closed, err := s.roundWasClosed(entry.ThreadID, entry.MessageID)
	if err != nil || !closed {
		t.Fatal("archived close was not found", closed, err)
	}
	closed, err = s.roundWasClosed(entry.ThreadID, "another-message")
	if err != nil || closed {
		t.Fatal("unrelated archived close matched", closed, err)
	}
	closedRounds, err := f.manager.closedRoundHistory()
	if err != nil || len(closedRounds) != 1 || closedRounds[0].Thread != entry.ThreadID || closedRounds[0].Trace != entry.MessageID {
		t.Fatal("archived close was not passed to worker startup", closedRounds, err)
	}
}
