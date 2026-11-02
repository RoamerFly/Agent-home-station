package companion

import (
	"errors"
	"testing"
	"time"
)

func TestCacheIncomingObservationKeepsOriginalAndNeverResurrectsForgottenTurn(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now().UTC()
	original := ""
	in.Text = "[图片]"
	in.OriginalText = &original
	in.MessageTime = now.Format(time.RFC3339)
	in.Source = &SourceRef{
		InstanceID:  s.SourceInstanceID(),
		MessageID:   in.TraceID,
		ServerID:    "native-image",
		Account:     in.Account,
		Thread:      in.Thread,
		Sender:      in.Sender,
		MessageType: 3,
		OccurredAt:  in.MessageTime,
		Revision:    1,
		BodyHash:    "synthetic-image",
		Integrity:   "observed",
	}
	in.IncomingMedia = []IncomingMediaRef{{Source: *in.Source, Kind: "image", UploaderID: in.Sender, ObservationState: "pending", ObservationVersion: 1}}
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	ref, ok := s.SourceForInput(in)
	if !ok {
		t.Fatal("stored media source was unavailable")
	}
	if err := s.CacheIncomingObservation(in.Channel, *ref, "画面里有一只合成小猫"); err != nil {
		t.Fatal(err)
	}

	scope := s.Context(in, c, now)
	var got *Turn
	for i := range scope.Messages {
		if scope.Messages[i].EventID == ref.EventID {
			got = &scope.Messages[i]
			break
		}
	}
	if got == nil || got.Text != "[图片]" || got.FullText != "" || got.FullObservation != "画面里有一只合成小猫" {
		t.Fatalf("original body was changed or observation missing: %#v", got)
	}
	if len(got.IncomingMedia) != 1 || got.IncomingMedia[0].Observation != "画面里有一只合成小猫" || got.IncomingMedia[0].ObservationState != "observed" {
		t.Fatalf("media cache not attached to exact source: %#v", got.IncomingMedia)
	}

	stale := *ref
	stale.Revision++
	if err := s.CacheIncomingObservation(in.Channel, stale, "stale replacement"); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale source result = %v, want memory conflict", err)
	}
	if err := s.CacheIncomingObservation(in.Channel, *ref, " \n\t "); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("blank observation = %v, want memory conflict", err)
	}

	id := ScopeID(in.Channel, in.Account, in.Thread)
	if err := s.Clear(id); err != nil {
		t.Fatal(err)
	}
	// Clearing creates a recall boundary. A late parser completion is harmless:
	// it must neither fail the caller nor recreate the retained user event.
	if err := s.CacheIncomingObservation(in.Channel, *ref, "late parser output"); err != nil {
		t.Fatalf("forgotten source should be a no-op, got %v", err)
	}
	if after := s.Context(in, c, now); len(after.Messages) != 0 {
		t.Fatalf("late parser resurrected forgotten turn: %#v", after.Messages)
	}
}
