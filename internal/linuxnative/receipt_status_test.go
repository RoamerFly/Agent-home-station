package linuxnative

import "testing"

func TestVoiceReceiptNormalizationRequiresEveryDistinctNote(t *testing.T) {
	base := ToolStatus{State: "needs_review", VoiceChunks: []VoiceChunkStatus{{Index: 0, State: "accepted", HasReceipt: true, ServerID: "1"}, {Index: 1, State: "accepted", HasReceipt: true, ServerID: "2"}}}
	got := base.WithVoiceReceipts()
	if got.State != "accepted" || got.ServerID != "2" {
		t.Fatal(got)
	}
	for _, kind := range []string{"missing", "unknown", "no_receipt", "duplicate"} {
		bad := base
		bad.VoiceChunks = append([]VoiceChunkStatus(nil), base.VoiceChunks...)
		switch kind {
		case "missing":
			bad.VoiceChunks[1].ServerID = ""
		case "unknown":
			bad.VoiceChunks[1].State = "needs_review"
		case "no_receipt":
			bad.VoiceChunks[1].HasReceipt = false
		case "duplicate":
			bad.VoiceChunks[1].Index = 0
		}
		if bad.WithVoiceReceipts().State == "accepted" {
			t.Fatal(kind)
		}
	}
	if (ToolStatus{State: "unknown"}).WithVoiceReceipts().State != "unknown" {
		t.Fatal("empty job accepted")
	}
}
