package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

// timeoutAcceptedSurfPlatform deliberately models the only unsafe delivery
// outcome: the remote platform accepted a comment but its response was lost.
// Resume must therefore look up the durable request key instead of submitting
// the comment a second time.
type timeoutAcceptedSurfPlatform struct {
	submits          int
	lookups          int
	submittedKey     string
	submittedRequest surfInteractionRequest
}

func (p *timeoutAcceptedSurfPlatform) Allows(platform, account string) bool {
	return platform == "offline-platform" && account == "offline-account"
}

func (p *timeoutAcceptedSurfPlatform) Submit(_ context.Context, requestKey string, request surfInteractionRequest) (surfInteractionReceipt, error) {
	p.submits++
	p.submittedKey = requestKey
	p.submittedRequest = request
	return surfInteractionReceipt{}, errors.New("synthetic timeout after remote acceptance")
}

func (p *timeoutAcceptedSurfPlatform) Lookup(_ context.Context, requestKey string) (surfInteractionReceipt, error) {
	p.lookups++
	if requestKey == "" || requestKey != p.submittedKey {
		return surfInteractionReceipt{}, errors.New("synthetic platform does not know this request key")
	}
	return surfInteractionReceipt{RequestKey: requestKey, OperationID: "offline-comment-accepted-1", State: "accepted"}, nil
}

func installOfflineInteractionGrant(t *testing.T, f *webFixture, p *permittedTools, source mcptools.DiscoverySource, session *surfSession) {
	t.Helper()
	f.manager.mu.Lock()
	f.manager.surf.SettingsRevision++
	f.manager.surf.Settings.InteractionGrants = []surfInteractionGrant{{
		SourceID: source.ID, Platform: "offline-platform", PlatformAccount: "offline-account", Actions: []string{"comment"}, Enabled: true,
	}}
	f.manager.mu.Unlock()
	account, epoch, scope, interest, err := p.surfExplorationIdentity(context.Background(), p.trace.SurfTopic)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.surfHistory.mu.Lock()
	defer f.manager.surfHistory.mu.Unlock()
	for i := range f.manager.surfHistory.data.Sessions {
		if f.manager.surfHistory.data.Sessions[i].ID == session.ID {
			f.manager.surfHistory.data.Sessions[i].Account = account
			f.manager.surfHistory.data.Sessions[i].AuthorizationEpoch = epoch
			f.manager.surfHistory.data.Sessions[i].ScopeKey = scope
			f.manager.surfHistory.data.Sessions[i].InterestKey = interest
			*session = f.manager.surfHistory.data.Sessions[i]
			return
		}
	}
	t.Fatal("synthetic surf session disappeared")
}

func decodeSurfInteractionOutput(t *testing.T, raw string) struct {
	RequestKey string `json:"request_key"`
	State      string `json:"state"`
	ReceiptID  string `json:"operation_id"`
} {
	t.Helper()
	var out struct {
		RequestKey string `json:"request_key"`
		State      string `json:"state"`
		ReceiptID  string `json:"operation_id"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("invalid surf interaction result %q: %v", raw, err)
	}
	return out
}

// setupInteractionReplay creates an ordinary autonomous exploration that has
// actually read one page. It intentionally uses the same completed web-job
// evidence as the normal reader, rather than letting a test fabricate a URL
// or an adapter display name as permission to interact.
func setupInteractionReplay(t *testing.T) (*webFixture, *permittedTools, mcptools.DiscoverySource, surfSession) {
	t.Helper()
	ctx := context.Background()
	f, p := trustedAgent(t)
	store, installed := installPublicWebFixture(t, f)
	source := configureSyntheticSurf(t, f, p)
	f.manager.config.Companion.Permissions.Remember = true
	p.trace.SurfTopic = "海岸潮汐"
	p.trace.Text = "自主探索海岸潮汐"
	if err := f.manager.memory.Observe(p.trace, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	account, epoch, scope, interest, err := p.surfExplorationIdentity(ctx, p.trace.SurfTopic)
	if err != nil {
		t.Fatal(err)
	}
	job := completedRecallReadJob(t, ctx, store, installed, p, "https://example.test/tide", "潮水在夜色里泛着蓝光。", 0)
	candidate := mcptools.DiscoveryItem{ID: "tide", Title: "蓝色潮汐", URL: "https://example.test/tide", Source: "伪造显示名称不授权", SourceID: source.ID}
	session := surfSession{
		ID: p.trace.TraceID, Account: account, Thread: p.trace.Thread, AuthorizationEpoch: epoch,
		ScopeKey: scope, Interest: p.trace.SurfTopic, InterestKey: interest, Candidates: []mcptools.DiscoveryItem{candidate},
		Observations: []surfObservation{{CandidateID: candidate.ID, Platform: source.Name, URL: candidate.URL, Tool: "web_fetch", State: "body_read", JobID: job.ID, ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReturnedCharacters: len([]rune("潮水在夜色里泛着蓝光。")), TotalCharacters: len([]rune("潮水在夜色里泛着蓝光。")), Complete: true, At: time.Now().UTC()}},
	}
	f.manager.surfHistory.mu.Lock()
	f.manager.surfHistory.data.Sessions = []surfSession{session}
	f.manager.surfHistory.mu.Unlock()
	return f, p, source, session
}

// TestSurfInteractionReplayRequiresCurrentExplicitGrant proves the pre-submit
// half of the offline interaction scenario. It catches a particularly risky
// regression: a provider label, a discovered card, or a prior read must not
// silently turn into permission to comment. Revoking that exact grant or
// clearing the conversation invalidates the generation stamp and the stored
// request before a later recovery step can submit it.
func TestSurfInteractionReplayRequiresCurrentExplicitGrant(t *testing.T) {
	f, p, source, session := setupInteractionReplay(t)
	ctx := context.Background()

	if _, err := p.surfInteractionSource(ctx, "tide"); err == nil || !strings.Contains(err.Error(), "authorization") {
		t.Fatalf("read candidate without an explicit interaction grant was usable: %v", err)
	}
	if tools := p.surfInteractionTools(); len(tools) != 0 {
		t.Fatalf("ungranted source exposed a state-changing tool: %+v", tools)
	}

	f.manager.mu.Lock()
	f.manager.surf.SettingsRevision++
	f.manager.surf.Settings.InteractionGrants = []surfInteractionGrant{{SourceID: source.ID, Platform: "offline-platform", PlatformAccount: "offline-account", Actions: []string{"comment"}, Enabled: true}}
	f.manager.mu.Unlock()
	// The grant revision is deliberately part of an exploration scope. A real
	// round starts after the grant is configured, so update this synthetic
	// session to the current host identity before asking it to act.
	account, epoch, scope, interest, err := p.surfExplorationIdentity(ctx, p.trace.SurfTopic)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.surfHistory.mu.Lock()
	for i := range f.manager.surfHistory.data.Sessions {
		if f.manager.surfHistory.data.Sessions[i].ID == session.ID {
			f.manager.surfHistory.data.Sessions[i].Account = account
			f.manager.surfHistory.data.Sessions[i].AuthorizationEpoch = epoch
			f.manager.surfHistory.data.Sessions[i].ScopeKey = scope
			f.manager.surfHistory.data.Sessions[i].InterestKey = interest
		}
	}
	f.manager.surfHistory.mu.Unlock()
	stamp := p.surfInteractionGenerationStamp()
	if stamp == "" {
		t.Fatal("explicit grant did not enter the autonomous generation stamp")
	}
	origin, err := p.surfInteractionSource(ctx, "tide")
	if err != nil {
		t.Fatal("current grant rejected the verified read", err)
	}
	if origin.Platform != "offline-platform" || origin.PlatformAccount != "offline-account" || origin.SourceJobID == "" || origin.SourceContentHash == "" || origin.SessionID != session.ID {
		t.Fatalf("source provenance was weakened: %+v", origin)
	}
	request := origin.request("这段潮声很安静。")
	if !p.surfInteractionGrantCurrent(request) {
		t.Fatal("exact current interaction request was not revalidated")
	}

	// A source-label change does not matter because the configured source ID is
	// bound. Removing the grant does matter immediately, including for an
	// already-assembled generation snapshot.
	f.manager.mu.Lock()
	f.manager.surf.SettingsRevision++
	f.manager.surf.Settings.InteractionGrants = nil
	f.manager.mu.Unlock()
	if p.surfInteractionGenerationStamp() == stamp {
		t.Fatal("removed grant left the old generation snapshot usable")
	}
	if p.surfInteractionGrantCurrent(request) {
		t.Fatal("removed grant left a persisted interaction request usable")
	}
	if _, err := p.surfInteractionSource(ctx, "tide"); err == nil {
		t.Fatal("removed grant still allowed a new interaction")
	}

	// Restore an otherwise identical grant, then clear the source conversation.
	// The new settings revision cannot resurrect the old request and the clear
	// invalidates the underlying body-read evidence as well.
	f.manager.mu.Lock()
	f.manager.surf.SettingsRevision++
	f.manager.surf.Settings.InteractionGrants = []surfInteractionGrant{{SourceID: source.ID, Platform: "offline-platform", PlatformAccount: "offline-account", Actions: []string{"comment"}, Enabled: true}}
	f.manager.mu.Unlock()
	if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	if p.surfInteractionGenerationStamp() == stamp || p.surfInteractionGrantCurrent(origin.request("这段潮声很安静。")) {
		t.Fatal("cleared conversation kept the old interaction generation or request authority")
	}
}

// TestSurfInteractionReplayTimeoutAcceptedOnlyLooksUpOnResume exercises the
// ordinary discovered tool boundary end-to-end. The scripted platform accepts
// the first submit but simulates a lost response. Until an authoritative
// lookup returns a matching receipt, no "I commented" experience is visible
// to the next exploration. A resumed round is lookup-only and never sends the
// same public interaction again.
func TestSurfInteractionReplayTimeoutAcceptedOnlyLooksUpOnResume(t *testing.T) {
	f, p, source, session := setupInteractionReplay(t)
	ctx := context.Background()
	platform := &timeoutAcceptedSurfPlatform{}
	p.surfInteractionAdapter = platform

	// An installed adapter and a discovered card alone cannot authorize a write.
	if _, err := p.Call(ctx, "surf_interaction", json.RawMessage(`{"action":"comment","candidate_id":"tide","text":"潮声很安静。"}`)); err == nil {
		t.Fatal("ungranted normal tool call unexpectedly succeeded")
	}
	if platform.submits != 0 || platform.lookups != 0 {
		t.Fatalf("ungranted tool call reached platform: submits=%d lookups=%d", platform.submits, platform.lookups)
	}

	installOfflineInteractionGrant(t, f, p, source, &session)
	fork := p.ForkTools().(*permittedTools)
	fork.SetTrace(p.trace)
	p = fork
	p.trace.Preview = true
	if _, err := p.Call(ctx, "surf_interaction", json.RawMessage(`{"action":"comment","candidate_id":"tide","text":"preview"}`)); err == nil || platform.submits != 0 {
		t.Fatal("preview allowed an external comment")
	}
	p.trace.Preview = false
	tools, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools {
		if tool.Name == "surf_interaction" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("normal tool discovery did not expose authorized interaction tool")
	}

	firstRaw, err := p.Call(ctx, "surf_interaction", json.RawMessage(`{"action":"comment","candidate_id":"tide","text":"潮声很安静。"}`))
	if err != nil {
		t.Fatalf("timeout-safe comment invocation returned error instead of durable unknown state: %v", err)
	}
	first := decodeSurfInteractionOutput(t, firstRaw)
	if first.RequestKey == "" || first.State != "submitted_unknown" || first.ReceiptID != "" {
		t.Fatalf("lost-response submit was not persisted as lookup-only unknown: %+v", first)
	}
	if platform.submits != 1 || platform.lookups != 0 {
		t.Fatalf("first submit counters: submits=%d lookups=%d", platform.submits, platform.lookups)
	}
	if platform.submittedKey != first.RequestKey || platform.submittedRequest.CandidateID != "tide" || platform.submittedRequest.Text != "潮声很安静。" {
		t.Fatalf("adapter did not receive the durable original operation: key=%q request=%+v", platform.submittedKey, platform.submittedRequest)
	}
	if got := p.priorConfirmedInteractions(ctx, session); len(got) != 0 {
		t.Fatalf("unknown submit became confirmed experience before receipt: %+v", got)
	}

	// A later autonomous trace for the same still-authorized exploration is the
	// recovery boundary. It uses the durable host request key, never an adapter
	// operation ID guessed from the failed submit.
	resume := session
	resume.ID = "surf-resume-after-timeout"
	f.manager.surfHistory.mu.Lock()
	f.manager.surfHistory.data.Sessions = append(f.manager.surfHistory.data.Sessions, resume)
	f.manager.surfHistory.mu.Unlock()
	resumedTrace := p.trace
	resumedTrace.TraceID = resume.ID
	p.SetTrace(resumedTrace)
	pendingRaw, err := p.Call(ctx, "web_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("normal discovery did not surface lookup-only pending interaction: %v", err)
	}
	var pending struct {
		Pending []struct {
			RequestKey string `json:"request_key"`
			State      string `json:"state"`
		} `json:"pending_public_interactions"`
	}
	if err := json.Unmarshal([]byte(pendingRaw), &pending); err != nil || len(pending.Pending) != 1 || pending.Pending[0].RequestKey != first.RequestKey || pending.Pending[0].State != "submitted_unknown" {
		t.Fatalf("normal discovery did not expose only the durable lookup handle: result=%s err=%v", pendingRaw, err)
	}
	statusRaw, err := p.Call(ctx, "surf_interaction", json.RawMessage(`{"action":"status","request_key":"`+first.RequestKey+`"}`))
	if err != nil {
		t.Fatalf("authorized resume lookup failed: %v", err)
	}
	settled := decodeSurfInteractionOutput(t, statusRaw)
	if settled.RequestKey != first.RequestKey || settled.State != "accepted" || settled.ReceiptID != "offline-comment-accepted-1" {
		t.Fatalf("resume did not settle the exact original interaction: %+v", settled)
	}
	if platform.submits != 1 || platform.lookups != 1 {
		t.Fatalf("resume resubmitted instead of lookup-only recovery: submits=%d lookups=%d", platform.submits, platform.lookups)
	}
	confirmed := p.priorConfirmedInteractions(ctx, resume)
	if len(confirmed) != 1 || confirmed[0].CandidateID != "tide" || confirmed[0].Platform != "offline-platform" || confirmed[0].PlatformAccount != "offline-account" || confirmed[0].ReceiptID != "offline-comment-accepted-1" {
		t.Fatalf("accepted receipt was not projected as the verified experience: %+v", confirmed)
	}
	confirmedRaw, err := p.Call(ctx, "web_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("normal discovery did not surface accepted experience: %v", err)
	}
	var surfaced struct {
		Confirmed []struct {
			CandidateID string `json:"candidate_id"`
			ReceiptID   string `json:"receipt_id"`
		} `json:"prior_confirmed_interactions"`
	}
	if err := json.Unmarshal([]byte(confirmedRaw), &surfaced); err != nil || len(surfaced.Confirmed) != 1 || surfaced.Confirmed[0].CandidateID != "tide" || surfaced.Confirmed[0].ReceiptID != "offline-comment-accepted-1" {
		t.Fatalf("normal discovery did not surface only the confirmed receipt: result=%s err=%v", confirmedRaw, err)
	}

	// Clearing conversation memory immediately invalidates the old evidence and
	// hides the projected experience. It also prevents status from looking up
	// the already accepted operation after the source has been forgotten.
	if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	if got := p.priorConfirmedInteractions(ctx, resume); len(got) != 0 {
		t.Fatalf("cleared source conversation still projected confirmed experience: %+v", got)
	}
	if _, err := p.Call(ctx, "surf_interaction", json.RawMessage(`{"action":"status","request_key":"`+first.RequestKey+`"}`)); err == nil {
		t.Fatal("cleared source conversation still allowed interaction status lookup")
	}
	if platform.submits != 1 || platform.lookups != 1 {
		t.Fatalf("clear caused another interaction transport call: submits=%d lookups=%d", platform.submits, platform.lookups)
	}

	// Explicit revocation is independently immediate. It must block both a
	// generation hint and any old durable request, without submitting again.
	f.manager.mu.Lock()
	f.manager.surf.SettingsRevision++
	f.manager.surf.Settings.InteractionGrants = nil
	f.manager.mu.Unlock()
	if p.surfInteractionGenerationStamp() != "" {
		t.Fatal("removed grant remained visible in generation state")
	}
	if _, err := p.Call(ctx, "surf_interaction", json.RawMessage(`{"action":"status","request_key":"`+first.RequestKey+`"}`)); err == nil {
		t.Fatal("revoked grant still allowed interaction status lookup")
	}
	if platform.submits != 1 || platform.lookups != 1 {
		t.Fatalf("revocation caused another interaction transport call: submits=%d lookups=%d", platform.submits, platform.lookups)
	}
}
