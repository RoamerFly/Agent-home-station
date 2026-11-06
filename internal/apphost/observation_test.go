package apphost

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type observationFixture struct {
	mu      sync.Mutex
	allowed bool
	version string
	pages   map[string]ObservationPage
	cursors []string
}

func (f *observationFixture) Connections(context.Context) ([]Connection, error) {
	return []Connection{{ID: "account", AccountID: "owner", Configuration: "ready"}}, nil
}
func (f *observationFixture) Inspect(context.Context, string) (ConnectionStatus, error) {
	return ConnectionStatus{}, nil
}
func (f *observationFixture) ObservationTargets(context.Context, string) ([]ObservationTarget, error) {
	return []ObservationTarget{{ContentID: "post", Label: "private title"}}, nil
}
func (f *observationFixture) ObservationGrant(context.Context, ObservationAccess) (ObservationGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ObservationGrant{Allowed: f.allowed, Version: f.version}, nil
}
func (f *observationFixture) ReadObservation(_ context.Context, r ObservationRequest) (ObservationPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors = append(f.cursors, r.Cursor)
	page, ok := f.pages[r.Cursor]
	if !ok {
		return ObservationPage{}, errors.New("missing page")
	}
	return page, nil
}

func observerFixtureRegistry(t *testing.T, f *observationFixture) Registry {
	t.Helper()
	r, err := New(Registration{Application: Application{ID: "offline", Name: "离线", InstallationID: "offline-test", Registration: "test"}, Provider: f})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestObserverResumesAndRechecksGrant(t *testing.T) {
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{
		"":     {Chunks: []ObservationChunk{{Position: 0, Text: "first"}}, Coverage: "body:page-1", NextCursor: "next"},
		"next": {Chunks: []ObservationChunk{{Position: 1, Text: "second"}}, Coverage: "body:complete", Complete: true},
	}}
	dir := t.TempDir()
	input := ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationBody, Limit: 1}
	first, err := NewObserver(observerFixtureRegistry(t, f), dir).Read(t.Context(), input)
	if err != nil || first.Complete || first.Resumed || len(first.Chunks) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := NewObserver(observerFixtureRegistry(t, f), dir).Read(t.Context(), input)
	if err != nil || !second.Complete || !second.Resumed || second.Chunks[0].Text != "second" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	f.mu.Lock()
	got := append([]string(nil), f.cursors...)
	f.allowed = false
	f.mu.Unlock()
	if len(got) != 2 || got[0] != "" || got[1] != "next" {
		t.Fatalf("cursors=%q", got)
	}
	if err := NewObserver(observerFixtureRegistry(t, f), dir).Check(t.Context(), second); !errors.Is(err, ErrObservationForbidden) {
		t.Fatalf("revoked check=%v", err)
	}
}

func TestObserverRejectsNoProgress(t *testing.T) {
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{"": {Chunks: []ObservationChunk{{Text: "partial"}}, Coverage: "body:partial"}}}
	_, err := NewObserver(observerFixtureRegistry(t, f), t.TempDir()).Read(t.Context(), ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationBody})
	if err == nil {
		t.Fatal("accepted a partial page without continuation")
	}
}

func TestObserverPlaybackProofSeparatesCoverageAndSampleCache(t *testing.T) {
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{}}
	observer := NewObserver(observerFixtureRegistry(t, f), t.TempDir())
	read := func(seconds int) (ObservationResult, error) {
		return observer.Read(t.Context(), ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationPlayback, Limit: seconds})
	}
	setPage := func(seconds int, ended bool, ranges ...PlaybackInterval) {
		f.pages[""] = ObservationPage{
			Coverage: "synthetic playback", Complete: true, Playback: ranges,
			PlaybackProof: &PlaybackProof{RequestedSeconds: seconds, MediaDurationSeconds: 3, Ready: true, Ended: ended, Paused: true, FullyPlayed: true},
		}
	}
	setPage(1, false, PlaybackInterval{0, 1})
	first, err := read(1)
	if err != nil || first.PlaybackProof == nil || first.PlaybackProof.FullyPlayed || !first.Complete {
		t.Fatalf("completed sample falsely claimed full playback: %#v %v", first, err)
	}
	if cached, err := read(1); err != nil || !cached.Cached || cached.PlaybackProof.FullyPlayed {
		t.Fatalf("exact cache lost sampled evidence: %#v %v", cached, err)
	}
	setPage(2, true, PlaybackInterval{0, 1}, PlaybackInterval{2, 3})
	gap, err := read(2)
	if err != nil || gap.Cached || gap.PlaybackProof == nil || gap.PlaybackProof.FullyPlayed {
		t.Fatalf("a new sample with a coverage gap was treated as full: %#v %v", gap, err)
	}
	setPage(3, true, PlaybackInterval{0, 3})
	full, err := read(3)
	if err != nil || full.Cached || full.PlaybackProof == nil || !full.PlaybackProof.FullyPlayed {
		t.Fatalf("a verified full interval was lost: %#v %v", full, err)
	}
	setPage(4, false)
	for attempt := 0; attempt < 2; attempt++ {
		ready, err := read(4)
		if err != nil || ready.Cached || ready.PlaybackProof == nil || !ready.PlaybackProof.Ready || ready.PlaybackProof.FullyPlayed || len(ready.Playback) != 0 {
			t.Fatalf("readiness was cached or turned into playback: %#v %v", ready, err)
		}
	}
}

func TestObservationAudioProofBindsWindowAndSource(t *testing.T) {
	proof := AudioProof{
		State: "transcribed", MediaSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		RequestedSeconds: 3, WindowEndSeconds: 3, DecodedSeconds: 2.75, MediaDurationSeconds: 6,
	}
	page := ObservationPage{
		AudioProof: &proof, Chunks: []ObservationChunk{{Text: "synthetic words"}},
		Coverage: "synthetic decode", NextCursor: "next",
	}
	if err := validateObservationPage(ObservationAudio, page); err != nil {
		t.Fatal(err)
	}
	if err := validateObservationPage(ObservationComments, page); err == nil {
		t.Fatal("audio evidence was accepted as comments")
	}
	short := proof
	short.DecodedSeconds = 5
	bad := page
	bad.AudioProof = &short
	if err := validateObservationPage(ObservationAudio, bad); err == nil {
		t.Fatal("decoded duration exceeded the observation window")
	}

	secondProof := proof
	secondProof.StartSeconds, secondProof.WindowEndSeconds = 3, 6
	secondProof.MediaSHA256 = "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	second := ObservationPage{AudioProof: &secondProof, Chunks: page.Chunks, Coverage: page.Coverage, Complete: true}
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{"": page, "next": second}}
	observer := NewObserver(observerFixtureRegistry(t, f), t.TempDir())
	input := ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationAudio, Limit: 3}
	if result, err := observer.Read(t.Context(), input); err != nil || result.AudioProof == nil || result.Complete {
		t.Fatalf("first result=%+v error=%v", result, err)
	}
	if _, err := observer.Read(t.Context(), input); !errors.Is(err, ErrObservationAudioSourceChanged) {
		t.Fatalf("swapped media accepted: %v", err)
	}
	secondProof.MediaSHA256 = proof.MediaSHA256
	if result, err := observer.Read(t.Context(), input); err != nil || !result.Resumed || !result.Complete {
		t.Fatalf("verified retry=%+v error=%v", result, err)
	}
}

func TestObservationSearchRefreshRestartsOnlyOnRequest(t *testing.T) {
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{
		"":     {Chunks: []ObservationChunk{{Text: "first"}}, Coverage: "first page", NextCursor: "next"},
		"next": {Chunks: []ObservationChunk{{Text: "last"}}, Coverage: "last page", Complete: true},
	}}
	observer := NewObserver(observerFixtureRegistry(t, f), t.TempDir())
	input := ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationSearch}
	if page, err := observer.Read(t.Context(), input); err != nil || page.Resumed || page.Complete {
		t.Fatalf("initial search = %+v, %v", page, err)
	}
	if page, err := observer.Read(t.Context(), input); err != nil || !page.Resumed || !page.Complete {
		t.Fatalf("continued search = %+v, %v", page, err)
	}
	if page, err := observer.Read(t.Context(), input); err != nil || !page.Cached {
		t.Fatalf("finished search = %+v, %v", page, err)
	}
	input.Refresh = true
	if page, err := observer.Read(t.Context(), input); err != nil || page.Resumed || page.Cached || page.Complete || page.Chunks[0].Text != "first" {
		t.Fatalf("refreshed search = %+v, %v", page, err)
	}
	input.Field = ObservationComments
	if _, err := observer.Read(t.Context(), input); !errors.Is(err, ErrObservationInvalid) {
		t.Fatalf("refresh on another field = %v", err)
	}
}

func TestOwnVideoObservationAlwaysReadsCurrentState(t *testing.T) {
	proof := OwnVideoProof{VideoID: "post", StatusCode: 4, State: "reviewing", ObservedAt: "2026-11-06T00:00:00Z"}
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{
		"": {OwnVideoProof: &proof, Coverage: "synthetic status", Complete: true},
	}}
	dir := t.TempDir()
	input := ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationOwnVideo}
	first, err := NewObserver(observerFixtureRegistry(t, f), dir).Read(t.Context(), input)
	if err != nil || first.OwnVideoProof == nil || first.OwnVideoProof.State != "reviewing" || first.Cached || len(first.Chunks) != 0 {
		t.Fatalf("reviewing result = %+v, %v", first, err)
	}
	public := OwnVideoProof{VideoID: "post", StatusCode: 5, State: "public", ObservedAt: "2026-11-06T00:01:00Z"}
	f.pages[""] = ObservationPage{OwnVideoProof: &public, Chunks: []ObservationChunk{{Text: "public title", SourceID: "post"}}, Coverage: "synthetic status", Complete: true}
	observer := NewObserver(observerFixtureRegistry(t, f), dir)
	result, err := observer.Read(t.Context(), input)
	if err != nil || result.Cached || result.OwnVideoProof == nil || result.OwnVideoProof.State != "public" || len(result.Chunks) != 1 {
		t.Fatalf("new state after restart = %+v, %v", result, err)
	}
	if first.OwnVideoProof.State != "reviewing" || len(f.cursors) != 2 {
		t.Fatalf("prior observation changed or skipped new request: first=%+v cursors=%v", first, f.cursors)
	}
	f.allowed = false
	if err := observer.Check(t.Context(), result); !errors.Is(err, ErrObservationForbidden) {
		t.Fatalf("revoked work status was usable: %v", err)
	}
}

func TestOwnVideoObservationRejectsUnboundProofAndPrivateMetadata(t *testing.T) {
	proof := &OwnVideoProof{VideoID: "other", StatusCode: 4, State: "reviewing", ObservedAt: "2026-11-06T00:00:00Z"}
	page := ObservationPage{OwnVideoProof: proof, Coverage: "synthetic status", Complete: true}
	f := &observationFixture{allowed: true, version: "v1", pages: map[string]ObservationPage{"": page}}
	observer := NewObserver(observerFixtureRegistry(t, f), t.TempDir())
	in := ObservationInput{AppID: "offline", ConnectionID: "account", ContentID: "post", Field: ObservationOwnVideo}
	if _, err := observer.Read(t.Context(), in); err == nil {
		t.Fatal("status of another video was accepted")
	}
	proof.VideoID = "post"
	page.Chunks = []ObservationChunk{{Text: "private title", SourceID: "post"}}
	f.pages[""] = page
	if _, err := observer.Read(t.Context(), in); err == nil {
		t.Fatal("unpublished title was accepted")
	}
	if err := validateObservationPage(ObservationSearch, ObservationPage{OwnVideoProof: proof, Complete: true}); err == nil {
		t.Fatal("own-video evidence was accepted in a search result")
	}
}
