package apphost

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"wechat-bot/internal/pluginhost"
)

type publicationFixtureProvider struct {
	mu      sync.Mutex
	grant   PublicationGrant
	submits int
	cancels int
	submit  func(PublicationRequest) PublicationReceipt
	query   func(PublicationReference) PublicationReceipt
	cancel  func(PublicationReference) PublicationReceipt
}

func (p *publicationFixtureProvider) Connections(context.Context) ([]Connection, error) {
	return []Connection{{ID: "account-1", AccountID: "social-owner", Configuration: "ready"}}, nil
}
func (p *publicationFixtureProvider) Inspect(context.Context, string) (ConnectionStatus, error) {
	return ConnectionStatus{}, nil
}
func (p *publicationFixtureProvider) PublicationGrant(context.Context, ConnectionRef) (PublicationGrant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.grant, nil
}
func (p *publicationFixtureProvider) SubmitPublication(_ context.Context, request PublicationRequest) (PublicationReceipt, error) {
	p.mu.Lock()
	p.submits++
	submit := p.submit
	p.mu.Unlock()
	if submit != nil {
		return submit(request), nil
	}
	return PublicationReceipt{Reference: withRemote(request.Reference, "remote-1"), State: "accepted"}, nil
}
func (p *publicationFixtureProvider) QueryPublication(_ context.Context, ref PublicationReference) (PublicationReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.query != nil {
		return p.query(ref), nil
	}
	return PublicationReceipt{Reference: ref, State: "unknown"}, nil
}
func (p *publicationFixtureProvider) CancelPublication(_ context.Context, ref PublicationReference) (PublicationReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancels++
	if p.cancel != nil {
		return p.cancel(ref), nil
	}
	return PublicationReceipt{Reference: ref, State: "unknown"}, nil
}
func withRemote(ref PublicationReference, id string) PublicationReference {
	ref.RemoteID = id
	return ref
}

func publicationActor(key string) pluginhost.Invocation {
	return pluginhost.Invocation{Account: "fish-1", Thread: "owner", Requester: "owner", Purpose: "publish prepared work", RequestKey: key, SourceVersions: map[string]int{"source-1": 1}, Authorization: "home-v1"}
}
func publicationArtifact() PublicationArtifact {
	return PublicationArtifact{ID: "asset-1", SHA256: strings.Repeat("a", 64), Name: "work.mp4", Bytes: 42}
}
func publicationDraft(target ConnectionRef) PublicationDraft {
	// Deliberately non-canonical/nested: pluginhost canonicalises Arguments;
	// the manifest must remain stable after that durable round trip.
	return PublicationDraft{Target: target, Title: "safe title", Body: "safe body", Artifacts: []PublicationArtifact{publicationArtifact()}, Origin: []byte(`{"z":{"b":2,"a":1},"source":"source-1","version":1}`)}
}

func newPublicationFixture(t *testing.T) (*Publisher, *publicationFixtureProvider, *pluginhost.Store, ConnectionRef) {
	t.Helper()
	return openPublicationFixture(t, t.TempDir())
}

func openPublicationFixture(t *testing.T, directory string) (*Publisher, *publicationFixtureProvider, *pluginhost.Store, ConnectionRef) {
	t.Helper()
	provider := &publicationFixtureProvider{grant: PublicationGrant{Allowed: true, Version: "home-v1"}}
	registry, err := New(Registration{Application: Application{ID: "social", Name: "Social", InstallationID: "social-install", Registration: "adapter-v1"}, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	store, err := pluginhost.OpenStore(directory, "fish-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	publisher := NewPublisher(registry, store, func(_ context.Context, _ pluginhost.Job, d PublicationDraft) error {
		if len(d.Artifacts) != 1 || d.Artifacts[0] != publicationArtifact() {
			return errors.New("not a verified artifact")
		}
		return nil
	}, func(context.Context, PublicationArtifact) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("synthetic")), nil
	})
	target, err := publisher.ResolveTarget(context.Background(), "social", "account-1")
	if err != nil {
		t.Fatal(err)
	}
	return publisher, provider, store, target
}

func TestPublicationPendingCancelNeverSubmits(t *testing.T) {
	publisher, provider, _, target := newPublicationFixture(t)
	ctx := context.Background()
	actor := publicationActor("pending-cancel")
	draft, err := publisher.Draft(ctx, actor, publicationDraft(target))
	if err != nil {
		t.Fatal(err)
	}
	view, err := publisher.Action(ctx, actor, draft.JobID, "cancel")
	if err != nil || view.State != "cancelled" || provider.submits != 0 {
		t.Fatalf("cancel=%+v submits=%d err=%v", view, provider.submits, err)
	}
}

func TestPublicationUnknownRestartQueriesWithoutResubmitting(t *testing.T) {
	directory := t.TempDir()
	publisher, provider, store, target := openPublicationFixture(t, directory)
	ctx := t.Context()
	actor := publicationActor("unknown-restart")
	input := publicationDraft(target)
	project := publicationArtifact()
	project.ID = "private-project"
	input.Project = &project
	var request PublicationRequest
	provider.submit = func(r PublicationRequest) PublicationReceipt {
		request = r
		if r.Draft.Project != nil || len(r.Draft.Origin) != 0 {
			t.Fatal("private source or project reached the provider")
		}
		if file, err := r.OpenArtifact(ctx, project); err == nil {
			file.Close()
			t.Fatal("provider opened private project")
		}
		file, err := r.OpenArtifact(ctx, publicationArtifact())
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(content) != "synthetic" {
			t.Fatalf("provider bytes=%q err=%v", content, err)
		}
		return PublicationReceipt{Reference: r.Reference, State: "unknown"}
	}
	draft, err := publisher.Draft(ctx, actor, input)
	if err != nil {
		t.Fatal(err)
	}
	view, err := publisher.Action(ctx, actor, draft.JobID, "submit")
	if err != nil || view.State != "awaiting_receipt" {
		t.Fatalf("submit=%+v %v", view, err)
	}
	provider.query = func(ref PublicationReference) PublicationReceipt {
		return PublicationReceipt{Reference: withRemote(ref, "remote-late"), State: "published"}
	}
	if file, err := request.OpenArtifact(ctx, publicationArtifact()); err == nil {
		file.Close()
		t.Fatal("provider retained file authority after submit")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := pluginhost.OpenStore(directory, "fish-1")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewPublisher(publisher.registry, reopened, publisher.validate, publisher.open)
	view, err = restarted.Action(ctx, actor, draft.JobID, "status")
	if err != nil || view.State != "published" || provider.submits != 1 {
		t.Fatalf("recovery=%+v submits=%d err=%v", view, provider.submits, err)
	}
}

func TestPublicationInflightCancelKeepsLatePublishedReceipt(t *testing.T) {
	publisher, provider, _, target := newPublicationFixture(t)
	ctx := context.Background()
	actor := publicationActor("inflight-cancel")
	draft, err := publisher.Draft(ctx, actor, publicationDraft(target))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.Action(ctx, actor, draft.JobID, "submit"); err != nil {
		t.Fatal(err)
	}
	provider.cancel = func(ref PublicationReference) PublicationReceipt {
		return PublicationReceipt{Reference: ref, State: "unknown"}
	}
	provider.query = func(ref PublicationReference) PublicationReceipt {
		return PublicationReceipt{Reference: withRemote(ref, "remote-1"), State: "published"}
	}
	view, err := publisher.Action(ctx, actor, draft.JobID, "cancel")
	if err != nil || view.State != "published" || !view.CancelRequested || provider.submits != 1 {
		t.Fatalf("cancel=%+v submits=%d err=%v", view, provider.submits, err)
	}
}

func TestPublicationRevocationAndOwnership(t *testing.T) {
	publisher, provider, _, target := newPublicationFixture(t)
	ctx := context.Background()
	actor := publicationActor("revoked")
	draft, err := publisher.Draft(ctx, actor, publicationDraft(target))
	if err != nil {
		t.Fatal(err)
	}
	provider.grant = PublicationGrant{Allowed: false}
	if _, err = publisher.Action(ctx, actor, draft.JobID, "submit"); !errors.Is(err, ErrPublicationDenied) || provider.submits != 0 {
		t.Fatalf("revoked submit err=%v count=%d", err, provider.submits)
	}
	other := actor
	other.Requester = "not-owner"
	if _, err = publisher.Action(ctx, other, draft.JobID, "status"); !errors.Is(err, ErrPublicationOwnership) {
		t.Fatalf("cross owner err=%v", err)
	}
}

func TestPublicationRejectsTamperedReceiptAndLocalDraftHasNoProvider(t *testing.T) {
	publisher, provider, _, target := newPublicationFixture(t)
	ctx := context.Background()
	actor := publicationActor("bad-receipt")
	provider.submit = func(r PublicationRequest) PublicationReceipt {
		r.Reference.ManifestSHA256 = strings.Repeat("b", 64)
		r.Reference.RemoteID = "wrong"
		return PublicationReceipt{Reference: r.Reference, State: "published"}
	}
	draft, err := publisher.Draft(ctx, actor, publicationDraft(target))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.Action(ctx, actor, draft.JobID, "submit"); !errors.Is(err, ErrPublicationReceipt) {
		t.Fatalf("tampered receipt err=%v", err)
	}
	localActor := publicationActor("local-draft")
	local, err := publisher.Draft(ctx, localActor, publicationDraft(ConnectionRef{}))
	if err != nil || local.State != "local_draft" || provider.submits != 1 {
		t.Fatalf("local=%+v submits=%d err=%v", local, provider.submits, err)
	}
	if _, err = publisher.Action(ctx, localActor, local.JobID, "submit"); !errors.Is(err, ErrPublicationDenied) {
		t.Fatalf("local submit err=%v", err)
	}
}

func TestPublicationInvalidArtifactRejectedBeforeTaskCreation(t *testing.T) {
	publisher, _, _, target := newPublicationFixture(t)
	draft := publicationDraft(target)
	draft.Artifacts[0].SHA256 = "bad"
	if _, err := publisher.Draft(context.Background(), publicationActor("invalid"), draft); !errors.Is(err, ErrPublicationInvalidDraft) {
		t.Fatalf("invalid draft err=%v", err)
	}
}

func TestPublicationConcurrentSubmitAndCancelClaimOnlyOnce(t *testing.T) {
	publisher, provider, _, target := newPublicationFixture(t)
	ctx := context.Background()
	actor := publicationActor("race-claim")
	draft, err := publisher.Draft(ctx, actor, publicationDraft(target))
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); _, _ = publisher.Action(ctx, actor, draft.JobID, "submit") }()
	go func() { defer group.Done(); _, _ = publisher.Action(ctx, actor, draft.JobID, "cancel") }()
	group.Wait()
	if provider.submits > 1 {
		t.Fatalf("duplicate submit=%d", provider.submits)
	}
	job, err := publisher.store.Job(ctx, draft.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !job.CancelRequested {
		t.Fatalf("concurrent submit lost cancellation: %#v", job)
	}
}
