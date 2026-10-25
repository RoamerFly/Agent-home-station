package admin

import (
	"testing"
	"wechat-bot/internal/bot"
)

func TestToolDiagnosticsPersistWithoutProviderData(t *testing.T) {
	f := setup(t)
	f.manager.record(bot.Record{Event: "web_activity", Category: "error", ThreadID: "filehelper", MessageID: "fixture", Tool: "web_fetch", Status: "failed", Reason: "连接提前中断", ToolCode: "network_closed", ToolStage: "response_body", ToolRequests: 2, ToolRetries: 1, ToolHTTPStatus: 503})
	if err := f.manager.loadJournal(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range f.manager.Logs() {
		if entry.Event == "web_activity" {
			found = true
			if entry.ToolCode != "network_closed" || entry.ToolStage != "response_body" || entry.ToolRequests != 2 || entry.ToolRetries != 1 || entry.ToolHTTPStatus != 503 || entry.Category != "error" {
				t.Fatal(entry)
			}
		}
	}
	if !found {
		t.Fatal("diagnostics not persisted")
	}
}
