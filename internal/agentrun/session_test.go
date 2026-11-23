package agentrun

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestFollowupIdentityDedupeConsumptionAndSeal(t *testing.T) {
	s := New(Scope{Account: "account", Thread: "group", Sender: "owner", RunID: "original"}, nil, nil)
	e := Event{ID: "followup", Thread: "group", Sender: "owner", Text: "change format"}
	other := e
	other.Sender = "member"
	if s.Offer("account", other) || s.Offer("wrong", e) {
		t.Fatal("identity boundary bypassed")
	}
	if !s.Offer("account", e) || !s.Offer("account", e) || s.Seal() {
		t.Fatal("pending event lost")
	}
	events, err := s.Drain(context.Background())
	if err != nil || len(events) != 1 || !s.Consumed(e.ID) || !s.Seal() {
		t.Fatal(events, err)
	}
	e.ID = "late"
	if s.Offer("account", e) {
		t.Fatal("sealed run captured a new independent message")
	}
}

func TestGroupFollowupKeepsEachSpeakersIdentity(t *testing.T) {
	s := New(Scope{Account: "account", Thread: "group", Sender: "owner", RunID: "original", AllowThreadPeers: true}, nil, nil)
	if !s.Offer("account", Event{ID: "correction", Thread: "group", Sender: "member", Text: "改一下"}) {
		t.Fatal("group peer correction was not offered to the live turn")
	}
	events, err := s.Drain(context.Background())
	if err != nil || len(events) != 1 || events[0].Sender != "member" {
		t.Fatal("group followup lost its sender", events, err)
	}
}

func TestOfferVersusSealHasOnlyOneOwner(t *testing.T) {
	for i := 0; i < 250; i++ {
		s := New(Scope{Account: "a", Thread: "t", Sender: "s", RunID: "r"}, nil, nil)
		var offered, sealed bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); offered = s.Offer("a", Event{ID: "next", Thread: "t", Sender: "s"}) }()
		go func() { defer wg.Done(); sealed = s.Seal() }()
		wg.Wait()
		if offered == sealed {
			t.Fatal("event lost or owned twice", offered, sealed)
		}
	}
}

func TestDrainFailureDoesNotClaimConsumptionAndBatchingDoesNotDrop(t *testing.T) {
	s := New(Scope{Account: "a", Thread: "t", Sender: "s", RunID: "r"}, nil, func(context.Context, []Event) error { return context.Canceled })
	s.Offer("a", Event{ID: "failed", Thread: "t", Sender: "s"})
	if _, err := s.Drain(context.Background()); err == nil || s.Consumed("failed") {
		t.Fatal("failed durable commit acknowledged")
	}
	s = New(Scope{Account: "a", Thread: "t", Sender: "s", RunID: "r"}, nil, nil)
	for i := 0; i < 19; i++ {
		s.Offer("a", Event{ID: fmt.Sprint(i), Thread: "t", Sender: "s"})
	}
	count := 0
	for s.Pending() {
		e, err := s.Drain(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		count += len(e)
	}
	if count != 19 || !s.Seal() {
		t.Fatal("batching lost input", count)
	}
}
