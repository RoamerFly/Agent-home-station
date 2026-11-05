package admin

import (
	"sync"
	"testing"
)

func syntheticSurfInteractionRequest() surfInteractionRequest {
	return surfInteractionRequest{
		Account: "test", Thread: "room@chatroom", SessionID: "surf-session", CandidateID: "candidate-one",
		Platform: "offline-platform", PlatformAccount: "fish-account", Action: "comment", Text: "这段潮汐记录真漂亮。",
		ScopeKey: "scope-current", InterestKey: "interest-tide", AuthorizationEpoch: "epoch-current", GrantID: "offline-source", Grant: "configured-offline-source", SettingsRevision: 7,
		SourceURL: "https://example.test/tide", SourceJobID: "read-job-one", SourceContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceStart: 0, SourceEnd: 28,
	}
}

func TestSurfInteractionUnknownReceiptNeverResubmitsAfterRestart(t *testing.T) {
	f := setup(t)
	request := syntheticSurfInteractionRequest()
	row, submit, err := f.manager.surfInteractions.claim(request)
	if err != nil || !submit || row.State != "submitted_unknown" {
		t.Fatalf("first claim = %+v submit=%v err=%v", row, submit, err)
	}
	// The process may die immediately after the platform accepts. On restart,
	// the durable unknown claim is lookup-only and may not become a new submit.
	reloaded, err := newSurfInteractionStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	row, submit, err = reloaded.claim(request)
	if err != nil || submit || row.State != "submitted_unknown" {
		t.Fatalf("restart claim = %+v submit=%v err=%v", row, submit, err)
	}
	if row, err = reloaded.settle(row.RequestKey, surfInteractionReceipt{RequestKey: row.RequestKey, OperationID: "platform-comment-1", State: "accepted"}); err != nil || row.State != "accepted" || row.ReceiptID != "platform-comment-1" || row.ConfirmedAt.IsZero() {
		t.Fatalf("accepted lookup = %+v err=%v", row, err)
	}
	if row, submit, err = reloaded.claim(request); err != nil || submit || row.State != "accepted" {
		t.Fatalf("accepted request became submit-ready: %+v submit=%v err=%v", row, submit, err)
	}
	confirmed := reloaded.confirmed(request.Account, request.Thread, request.ScopeKey, request.InterestKey, 10)
	if len(confirmed) != 1 || confirmed[0].RequestKey != row.RequestKey || confirmed[0].Request.SourceJobID != request.SourceJobID || confirmed[0].ReceiptID != "platform-comment-1" {
		t.Fatalf("confirmed projection = %+v", confirmed)
	}
}

func TestSurfInteractionReceiptBindsExactHostKeyAndOperation(t *testing.T) {
	f := setup(t)
	first := syntheticSurfInteractionRequest()
	firstRow, submit, err := f.manager.surfInteractions.claim(first)
	if err != nil || !submit {
		t.Fatal(firstRow, submit, err)
	}
	if _, err = f.manager.surfInteractions.settle(firstRow.RequestKey, surfInteractionReceipt{RequestKey: "different-host-key", OperationID: "platform-id", State: "accepted"}); err == nil {
		t.Fatal("foreign receipt key accepted")
	}
	if row, ok := f.manager.surfInteractions.read(firstRow.RequestKey, first.Account); !ok || row.State != "submitted_unknown" {
		t.Fatal("bad receipt changed durable state", row, ok)
	}
	if _, err = f.manager.surfInteractions.settle(firstRow.RequestKey, surfInteractionReceipt{RequestKey: firstRow.RequestKey, OperationID: "platform-id", State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.CandidateID = "candidate-two"
	second.Text = "另一条经过授权的评论。"
	second.SourceEnd++
	secondRow, submit, err := f.manager.surfInteractions.claim(second)
	if err != nil || !submit || secondRow.RequestKey == firstRow.RequestKey {
		t.Fatal(secondRow, submit, err)
	}
	if _, err = f.manager.surfInteractions.settle(secondRow.RequestKey, surfInteractionReceipt{RequestKey: secondRow.RequestKey, OperationID: "platform-id", State: "accepted"}); err == nil {
		t.Fatal("operation receipt reused by another request")
	}
	if rows := f.manager.surfInteractions.confirmed(first.Account, first.Thread, first.ScopeKey, first.InterestKey, 10); len(rows) != 1 || rows[0].RequestKey != firstRow.RequestKey {
		t.Fatal("foreign receipt leaked into confirmed experience", rows)
	}
}

func TestSurfInteractionChangedPayloadFindsOriginalActionAndFailsClosed(t *testing.T) {
	f := setup(t)
	request := syntheticSurfInteractionRequest()
	first, submit, err := f.manager.surfInteractions.claim(request)
	if err != nil || !submit {
		t.Fatal(first, submit, err)
	}
	changed := request
	changed.Text = "重试时不应重新生成一条评论。"
	changed.SourceEnd += 10
	if row, submit, err := f.manager.surfInteractions.claim(changed); err == nil || submit || row.RequestKey != "" {
		t.Fatalf("changed payload created a new logical action: %+v submit=%v err=%v", row, submit, err)
	}
	if row, ok := f.manager.surfInteractions.read(first.RequestKey, request.Account); !ok || row.State != "submitted_unknown" {
		t.Fatal("original unknown action was not retained", row, ok)
	}
}

func TestSurfInteractionClaimIsSingleSubmitAcrossConcurrentWorkers(t *testing.T) {
	f := setup(t)
	request := syntheticSurfInteractionRequest()
	const workers = 24
	var wg sync.WaitGroup
	var mu sync.Mutex
	submits, failures := 0, 0
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, submit, err := f.manager.surfInteractions.claim(request)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures++
			}
			if submit {
				submits++
			}
		}()
	}
	wg.Wait()
	if failures != 0 || submits != 1 {
		t.Fatalf("concurrent durable claims failures=%d submits=%d", failures, submits)
	}
}

func TestSurfInteractionClaimIsSingleSubmitAcrossReopenedHandles(t *testing.T) {
	f := setup(t)
	request := syntheticSurfInteractionRequest()
	second, err := newSurfInteractionStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	stores := []*surfInteractionStore{f.manager.surfInteractions, second}
	const workers = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	submits, failures := 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, submit, err := stores[i%len(stores)].claim(request)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures++
			}
			if submit {
				submits++
			}
		}(i)
	}
	wg.Wait()
	if failures != 0 || submits != 1 {
		t.Fatalf("reopened handles failures=%d submits=%d", failures, submits)
	}
	// A later trace must resolve to the existing durable unknown action. The
	// integration must lookup this original key rather than submit again.
	freshTrace := request
	freshTrace.SessionID = "next-surf-trace"
	freshTrace.Text = "改写后的评论不能成为新的提交。"
	row, submit, err := second.claim(freshTrace)
	if err != nil || submit || row.RequestKey != surfInteractionKey(request) || row.State != "submitted_unknown" {
		t.Fatalf("fresh trace did not resolve to original lookup: row=%+v submit=%v err=%v", row, submit, err)
	}
}

func TestSurfInteractionConfirmedProjectionFailsClosedOnScopeChange(t *testing.T) {
	f := setup(t)
	request := syntheticSurfInteractionRequest()
	row, _, err := f.manager.surfInteractions.claim(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.surfInteractions.settle(row.RequestKey, surfInteractionReceipt{RequestKey: row.RequestKey, OperationID: "platform-confirmed", State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []func(*surfInteractionRequest){
		func(r *surfInteractionRequest) { r.Account = "other" },
		func(r *surfInteractionRequest) { r.Thread = "other@chatroom" },
		func(r *surfInteractionRequest) { r.ScopeKey = "after-forget" },
		func(r *surfInteractionRequest) { r.InterestKey = "other-interest" },
	} {
		query := request
		changed(&query)
		if rows := f.manager.surfInteractions.confirmed(query.Account, query.Thread, query.ScopeKey, query.InterestKey, 3); len(rows) != 0 {
			t.Fatal("cross-scope confirmed interaction exposed", rows)
		}
	}
}
