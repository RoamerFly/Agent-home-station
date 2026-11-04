package admin

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
)

// TestPriorReadingExcerptsUsesOnlyCurrentAuthorizedExperience exercises the
// cross-exploration path without a model or real network. A completed web job
// is the only source permitted to re-enter the next same-interest exploration.
func TestPriorReadingExcerptsUsesOnlyCurrentAuthorizedExperience(t *testing.T) {
	ctx := context.Background()
	f, p := trustedAgent(t)
	store, installed := installPublicWebFixture(t, f)
	configureSyntheticSurf(t, f, p)
	f.manager.config.Companion.Permissions.Remember = true
	p.trace.Text = "关注海岸潮汐的摄影和阅读"
	p.trace.SurfTopic = "海岸潮汐摄影"
	generationGrant := p.MemoryResultAuthorization()
	if generationGrant == "" || !p.MemoryResultAuthorized(generationGrant) || p.memoryQueryAuthorization() != "" {
		t.Fatal("autonomous reading needs a generation stamp separate from member memory access")
	}
	if err := f.manager.memory.Observe(p.trace, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	account, epoch, scopeKey, interestKey, err := p.surfExplorationIdentity(ctx, p.trace.SurfTopic)
	if err != nil {
		t.Fatal(err)
	}
	current := surfSession{ID: p.trace.TraceID, Account: account, Thread: p.trace.Thread, AuthorizationEpoch: epoch, ScopeKey: scopeKey, Interest: p.trace.SurfTopic, InterestKey: interestKey}
	prior := current
	prior.ID = "surf-earlier-authorized-read"
	prior.Observations = []surfObservation{
		// Candidate metadata and failures remain in history for diagnostics, but
		// must never appear as a reread excerpt.
		{CandidateID: "card-only", URL: "https://example.test/card", Tool: "web_discover", State: "discovered", At: time.Now().UTC()},
		{CandidateID: "failed-read", URL: "https://example.test/failed", Tool: "web_fetch", State: "read_failed", Failure: "offline", At: time.Now().UTC()},
	}
	job := completedRecallReadJob(t, ctx, store, installed, p, "https://example.test/tide", "岸边蓝色潮汐", 0)
	prior.Observations = append(prior.Observations, surfObservation{CandidateID: "actually-read", URL: "https://example.test/tide", Platform: "synthetic-web", Tool: "web_fetch", State: "body_read", JobID: job.ID, ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReturnedCharacters: 6, TotalCharacters: 6, Complete: true, At: time.Now().UTC()})
	// Different trace but same account/thread/authorization epoch/interest is
	// eligible; a different account is not.
	foreign := prior
	foreign.ID, foreign.Account = "surf-foreign", "another-account"
	f.manager.surfHistory.mu.Lock()
	f.manager.surfHistory.data.Sessions = []surfSession{foreign, prior, current}
	f.manager.surfHistory.mu.Unlock()
	if !p.reusableSurfReadingScope(ctx, current) {
		gotAccount, gotEpoch, gotScope, gotInterest, identityErr := p.surfExplorationIdentity(ctx, current.Interest)
		t.Fatalf("current exploration scope unexpectedly unavailable: got=%q/%q/%q/%q err=%v want=%q/%q/%q/%q", gotAccount, gotEpoch, gotScope, gotInterest, identityErr, current.Account, current.AuthorizationEpoch, current.ScopeKey, current.InterestKey)
	}
	if rows := p.priorSurfReadingRows(current); len(rows) != 1 {
		t.Fatalf("prior row selection=%+v", rows)
	}
	if _, ok := p.validatedSurfReading(ctx, prior.Observations[2]); !ok {
		stored, _ := store.Job(ctx, job.ID)
		t.Fatalf("completed prior read did not validate: job=%+v policy=%v", stored, (servicePluginPolicy{p: p}).Check(ctx, stored, pluginhost.Capability{}))
	}

	got := p.priorReadingExcerpts(ctx, current)
	if len(got) != 1 || got[0].URL != "https://example.test/tide" || got[0].Platform != "synthetic-web" || got[0].Excerpt != "岸边蓝色潮汐" || got[0].Range != "characters 0-6 of 6" {
		t.Fatalf("only a verified prior page read may return: %+v", got)
	}
	p.trace.SurfTopic = "another unrelated interest"
	if p.MemoryResultAuthorized(generationGrant) {
		t.Fatal("old assembled context remained usable after switching interests")
	}
	if got := p.priorReadingExcerpts(ctx, current); len(got) != 0 {
		t.Fatalf("different current interest recalled old reading: %+v", got)
	}
	p.trace.SurfTopic = current.Interest
	f.manager.config.Companion.Permissions.Remember = false
	if p.MemoryResultAuthorized(generationGrant) {
		t.Fatal("old assembled context remained usable after disabling memory")
	}
	if got := p.priorReadingExcerpts(ctx, current); len(got) != 0 {
		t.Fatalf("disabled memory recalled old reading: %+v", got)
	}
	f.manager.config.Companion.Permissions.Remember = true

	if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	if got := p.priorReadingExcerpts(ctx, current); len(got) != 0 {
		t.Fatalf("cleared scope recalled old exploration text: %+v", got)
	}
}

func completedRecallReadJob(t *testing.T, ctx context.Context, store *pluginhost.Store, installed pluginhost.InstalledPackage, p *permittedTools, rawURL, excerpt string, offset int) pluginhost.Job {
	t.Helper()
	configuration, err := store.ConfigurationForExecution(ctx, installed.Package.ID)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := p.serviceEvidenceEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]any{"url": rawURL, "offset": offset, "limit": len([]rune(excerpt))})
	job, created, err := store.CreateJob(ctx, pluginhost.Job{
		Plugin: installed.Package.ID, Version: installed.Package.Version, PackageDigest: installed.Digest, ConfigRevision: strconv.Itoa(configuration.Revision),
		Tool: "web.read", Arguments: arguments,
		Context: pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.serviceRequester(), Purpose: "authorized same-scope exploration", RequestKey: "recall-reading-" + rawURL, EvidenceEpoch: epoch, Authorization: p.serviceAuthorization(), SourceVersions: map[string]int{}},
	})
	if err != nil || !created {
		t.Fatalf("create recall test job: created=%v err=%v", created, err)
	}
	job, err = store.ClaimJob(ctx, job.ID, job.Revision)
	if err == nil {
		job, err = store.AcknowledgeJob(ctx, job.ID, job.Revision, "synthetic-read")
	}
	if err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(serviceWebResult{OK: true, Untrusted: true, URL: rawURL, SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReadCoverage: &serviceWebReadCoverage{Kind: "page_body", StartOffset: offset, EndOffset: offset + len([]rune(excerpt)), TotalCharacters: len([]rune(excerpt)), Complete: true, Excerpt: excerpt}})
	job, err = store.CompleteJob(ctx, job.ID, job.Revision, nil, result)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
