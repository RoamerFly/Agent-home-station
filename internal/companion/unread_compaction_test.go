package companion

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCompactUnreadEvidenceKeepsRecentBatchIdentityWithinWireLimit(t *testing.T) {
	rows := make([]map[string]any, 43)
	for i := range rows {
		id := fmt.Sprintf("message-%d", i)
		rows[i] = map[string]any{
			"message_id": id, "message_sender_id": fmt.Sprintf("sender-%d", i%3),
			"at": "2026-11-24T14:30:00Z", "text": strings.Repeat("一段较长的真实消息内容。", 180),
			"source": &SourceRef{EventID: "event-" + id, MessageID: id, Account: "account", Thread: "room@chatroom", Sender: fmt.Sprintf("sender-%d", i%3), MessageType: 1, OccurredAt: "2026-11-24T14:30:00Z", BodyHash: strings.Repeat("a", 64)},
		}
	}
	got := compactUnreadEvidence(rows, 48<<10)
	encoded, err := json.Marshal(got)
	if err != nil || len(encoded) > 48<<10 || len(got) != 43 {
		t.Fatalf("compact batch: rows=%d bytes=%d error=%v", len(got), len(encoded), err)
	}
	for i, row := range got {
		source, ok := row["source"].(*SourceRef)
		if !ok || row["message_id"] != fmt.Sprintf("message-%d", i) || source.MessageID != row["message_id"] || source.Account != "account" || source.Thread != "room@chatroom" || row["message_sender_id"] == "" || row["at"] == "" {
			t.Fatalf("lost message provenance at row %d", i)
		}
	}
}
