package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestJobFailureDiagnosticSurvivesStoreRestartAndPagination(t *testing.T) {
	store, job, dir := privateResultFixture(t)
	ctx := context.Background()
	// Caller/plugin metadata is not part of the canonical job fingerprint and
	// cannot introduce a diagnostic while creating or retrieving an invocation.
	forged := job
	forged.Diagnostic = &JobDiagnostic{Stage: "plugin_supplied", Code: "synthetic-secret"}
	reused, created, err := store.CreateJob(ctx, forged)
	if err != nil || created || reused.Diagnostic != nil {
		t.Fatal("caller minted diagnostic", reused, created, err)
	}
	if _, err = store.ReviewJobFailure(ctx, job.ID, job.Revision, JobFailureStage("https://private.invalid/?key=synthetic-secret")); err == nil {
		t.Fatal("arbitrary stage accepted")
	}
	cancelled, err := store.RequestJobCancel(ctx, job.ID, job.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReviewJobFailure(ctx, job.ID, job.Revision, JobFailureAuthority); !errors.Is(err, ErrRevision) {
		t.Fatal("stale diagnostic bypassed CAS", err)
	}
	unchanged, err := store.Job(ctx, job.ID)
	if err != nil || unchanged.Diagnostic != nil || !unchanged.CancelRequested {
		t.Fatal("failed checkpoint changed job", unchanged, err)
	}
	finished, err := store.ReviewJobFailure(ctx, job.ID, cancelled.Revision, JobFailureAuthority)
	if err != nil || finished.State != "needs_review" || !finished.CancelRequested || len(finished.Result) != 0 {
		t.Fatal("review changed cancellation/result semantics", finished, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Job(ctx, job.ID)
	if err != nil || persisted.Diagnostic == nil || persisted.Diagnostic.Stage != JobFailureAuthority || persisted.Diagnostic.Code != "plugin_invocation_failed" || !persisted.CancelRequested || len(persisted.Result) != 0 {
		t.Fatal("reopen lost safe diagnostic", persisted, err)
	}
	page, err := reopened.Jobs(ctx, job.Plugin, 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Diagnostic == nil {
		t.Fatal("jobs API omitted diagnostic", page, err)
	}
	raw, err := json.Marshal(page)
	if err != nil || !strings.Contains(string(raw), `"diagnostic":{"stage":"authority","code":"plugin_invocation_failed"}`) || strings.Contains(string(raw), "synthetic-secret") {
		t.Fatal("unsafe public diagnostic serialization", string(raw), err)
	}
	if _, err = reopened.ClaimJob(ctx, job.ID, persisted.Revision); err == nil {
		t.Fatal("diagnostic made uncertain submission claimable")
	}
}

func TestLegacyJobJSONAndFinishRemainDiagnosticFree(t *testing.T) {
	var legacy Job
	if err := json.Unmarshal([]byte(`{"id":"old","state":"needs_review","revision":2}`), &legacy); err != nil || legacy.Diagnostic != nil || legacy.Recovery() != "needs_review" {
		t.Fatal("old record incompatible", legacy, err)
	}
	store, job, _ := privateResultFixture(t)
	finished, err := store.FinishJob(context.Background(), job.ID, job.Revision, "needs_review", nil)
	if err != nil || finished.Diagnostic != nil || len(finished.Result) != 0 {
		t.Fatal("legacy finish changed", finished, err)
	}
}

func TestJobDiagnosticTracksCurrentUncertaintyThroughRecovery(t *testing.T) {
	for _, action := range []string{"acknowledge_complete", "complete", "fail", "finish_completed", "finish_failed", "finish_cancelled", "finish_needs_review"} {
		t.Run(action, func(t *testing.T) {
			store, original, _ := privateResultFixture(t)
			ctx := context.Background()
			job, err := store.ReviewJobFailure(ctx, original.ID, original.Revision, JobFailureDispatch)
			if err != nil {
				t.Fatal(err)
			}
			// A request to cancel is not evidence that the uncertain operation
			// stopped; retain both the request and its diagnosis until resolved.
			job, err = store.RequestJobCancel(ctx, job.ID, job.Revision)
			if err != nil || job.Diagnostic == nil || job.State != "needs_review" {
				t.Fatal("cancel request hid uncertainty", job, err)
			}
			wantState := "completed"
			switch action {
			case "acknowledge_complete":
				job, err = store.AcknowledgeJob(ctx, job.ID, job.Revision, "verified-original-remote")
				if err != nil || job.Diagnostic != nil || job.State != "running" || job.Recovery() != "query" || !job.CancelRequested {
					t.Fatal("verified acknowledgement retained stale diagnostic", job, err)
				}
				job, err = store.CompleteJob(ctx, job.ID, job.Revision, []string{"verified-asset"}, json.RawMessage(`{"ok":true}`))
			case "complete":
				job, err = store.CompleteJob(ctx, job.ID, job.Revision, []string{"verified-asset"}, json.RawMessage(`{"ok":true}`))
			case "fail":
				wantState = "failed"
				job, err = store.FailJob(ctx, job.ID, job.Revision, json.RawMessage(`{"ok":false,"error":"verified_provider_failure"}`))
			default:
				wantState = strings.TrimPrefix(action, "finish_")
				job, err = store.FinishJob(ctx, job.ID, job.Revision, wantState, nil)
			}
			if err != nil || job.State != wantState || !job.CancelRequested {
				t.Fatal("recovery changed verified state or cancellation", job, err)
			}
			if (job.Diagnostic != nil) != (wantState == "needs_review") {
				t.Fatal("diagnostic does not match current uncertainty", job)
			}
			if action == "fail" && !strings.Contains(string(job.Result), "verified_provider_failure") {
				t.Fatal("verified failure result lost", job)
			}
			persisted, err := store.Job(ctx, job.ID)
			if err != nil || (persisted.Diagnostic != nil) != (wantState == "needs_review") {
				t.Fatal("persisted diagnostic is stale", persisted, err)
			}
			reused, created, err := store.CreateJob(ctx, original)
			if err != nil || created || reused.ID != job.ID || reused.State != wantState {
				t.Fatal("recovery lost original request binding", reused, created, err)
			}
			if _, err = store.ClaimJob(ctx, job.ID, job.Revision); err == nil {
				t.Fatal("recovered job can replay original submission")
			}
			if wantState != "needs_review" {
				if _, err = store.ReviewJobFailure(ctx, job.ID, job.Revision, JobFailureValidation); err == nil {
					t.Fatal("terminal job reacquired active failure")
				}
			}
		})
	}
}
