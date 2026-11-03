package companion

import (
	"errors"
	"testing"
	"time"
)

func TestAcceptedDeliveryDeduplicatesOnlyReceiptIdentity(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now().UTC()
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptedDelivery(in, "相同正文", "native:receipt-one", c, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// The same native receipt may be replayed after a restart, but it is still
	// one bubble in the transcript.
	if err := s.AcceptedDelivery(in, "相同正文", "native:receipt-one", c, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Equal words with a different receipt are a distinct, real send and must
	// remain visible to the model.
	if err := s.AcceptedDelivery(in, "相同正文", "native:receipt-two", c, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}

	var got []Turn
	for _, turn := range s.Context(in, c, now.Add(4*time.Second)).Messages {
		if turn.Role == "assistant" {
			got = append(got, turn)
		}
	}
	if len(got) != 2 {
		t.Fatalf("assistant bubbles = %#v, want two distinct accepted receipts", got)
	}
	if got[0].DeliveryID != "native:receipt-one" || got[1].DeliveryID != "native:receipt-two" || got[0].RequestTraceID != in.TraceID || got[1].RequestTraceID != in.TraceID {
		t.Fatalf("missing delivery/request linkage: %#v", got)
	}
	if err := s.AcceptedDelivery(in, "不同正文", "native:receipt-one", c, now.Add(5*time.Second)); !errors.Is(err, ErrDeliveryMemoryConflict) {
		t.Fatalf("receipt content conflict = %v, want %v", err, ErrDeliveryMemoryConflict)
	}
	if err := s.AcceptedDelivery(in, "无身份", "", c, now); !errors.Is(err, ErrDeliveryIdentityRequired) {
		t.Fatalf("empty identity = %v, want %v", err, ErrDeliveryIdentityRequired)
	}
}

func TestAcceptedDeliveryMigratesOnlyProvenLegacyReceiptKey(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now().UTC()
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	legacyReceipt := "delivery-verified-old-receipt"
	legacyPlanSlot := "utterance-legacy-0"
	id := ScopeID(in.Channel, in.Account, in.Thread)
	if err := s.scopeTransaction(id, func(data *persisted) error {
		scope := data.Scopes[id]
		scope.Messages = append(scope.Messages,
			Turn{TraceID: legacyReceipt, Role: "assistant", Text: "已送达", FullText: "已送达", Time: now},
			Turn{TraceID: legacyPlanSlot, Role: "assistant", Text: "已送达", FullText: "已送达", Time: now.Add(time.Second)},
		)
		data.Scopes[id] = scope
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptedDeliveryWithLegacyReceipt(in, "已送达", "native:current-receipt", legacyReceipt, c, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// A plan-slot record has no receipt proof and must remain a separate old
	// bubble rather than being silently merged merely because its text matches.
	if err := s.AcceptedDeliveryWithLegacyReceipt(in, "已送达", "native:second-receipt", "delivery-other-proof", c, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	var receipt, slot, second int
	for _, turn := range s.Context(in, c, now.Add(4*time.Second)).Messages {
		if turn.Role != "assistant" {
			continue
		}
		switch turn.DeliveryID {
		case "native:current-receipt":
			receipt++
			if turn.RequestTraceID != in.TraceID {
				t.Fatalf("migrated request trace = %q, want %q", turn.RequestTraceID, in.TraceID)
			}
		case "native:second-receipt":
			second++
		}
		if turn.DeliveryID == "" && turn.TraceID == legacyPlanSlot {
			slot++
		}
	}
	if receipt != 1 || slot != 1 || second != 1 {
		t.Fatalf("legacy migration merged an ambiguous record: receipt=%d slot=%d second=%d", receipt, slot, second)
	}
}
