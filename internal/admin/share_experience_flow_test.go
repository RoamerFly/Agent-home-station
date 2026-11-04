package admin

import (
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// TestShareExperienceFlowSeparatesCardCopyFromActualObservation exercises the
// handoff used after a sharing reader has produced its evidence.  A card
// description is useful context, but it is not an experience; visual and
// audio reads of the same shared asset are.  Keeping this at the admin/store
// boundary makes the distinction independent of a particular video provider.
func TestShareExperienceFlowSeparatesCardCopyFromActualObservation(t *testing.T) {
	f := setup(t)
	const (
		channel = "wechat"
		account = "test"
		thread  = "share-experience@chatroom"
		assetID = "same-shared-video"
	)

	cardMessage := linuxnative.Message{Type: 49, Link: &linuxnative.LinkCard{Title: "海边散步", Description: "这是关于海边散步的视频。", SourceName: "合成发布者"}}
	publisherPart, ok := publisherDescriptionObservation(cardMessage)
	if !ok {
		t.Fatal("publisher description was not classified")
	}
	publisher := publisherPart.Excerpt
	card := observeSharedVideoForExperience(t, f, channel, account, thread, "share-card-only", assetID,
		"[分享来源资料] "+publisher,
		publisherPart.Range,
		publisher,
		[]companion.ObservationPart{publisherPart})
	visualText := "画面实际观察：傍晚的潮水拍向礁石。"
	visualEvidence := trustedVideoEvidence(videoObservationCoverage(48, []float64{4, 18, 36}, true, 0, false, false), visualText, "")
	if len(visualEvidence.Parts) != 1 || visualEvidence.Parts[0].Kind != companion.ObservationVisualFrames {
		t.Fatalf("visual reader did not create typed evidence: %#v", visualEvidence)
	}
	visual := observeSharedVideoForExperience(t, f, channel, account, thread, "share-visual", assetID,
		"[分享来源资料] "+publisher+"\n[实际画面观察] "+visualText,
		visualEvidence.ObservedRange,
		visualEvidence.Excerpt,
		[]companion.ObservationPart{
			publisherPart,
			visualEvidence.Parts[0],
		})
	audioText := "实际音轨转写：海风很大，我们沿着海边慢慢走。"
	audioEvidence := trustedVideoEvidence(videoObservationCoverage(48, nil, false, 48, true, false), "", audioText)
	if len(audioEvidence.Parts) != 1 || audioEvidence.Parts[0].Kind != companion.ObservationAudioTranscript {
		t.Fatalf("audio reader did not create typed evidence: %#v", audioEvidence)
	}
	audio := observeSharedVideoForExperience(t, f, channel, account, thread, "share-audio", assetID,
		"[分享来源资料] "+publisher+"\n[实际音轨转写] "+audioText,
		audioEvidence.ObservedRange,
		audioEvidence.Excerpt,
		[]companion.ObservationPart{
			publisherPart,
			audioEvidence.Parts[0],
		})
	// When both readers finish for one share, the durable candidate must retain
	// both actual ranges and both excerpts. It must not collapse to whichever
	// tool happened to finish last, nor promote the card description.
	bothEvidence := trustedVideoEvidence(videoObservationCoverage(48, []float64{4, 18, 36}, true, 48, true, false), visualText, audioText)
	if len(bothEvidence.Parts) != 2 || bothEvidence.Parts[0].Kind != companion.ObservationVisualFrames || bothEvidence.Parts[1].Kind != companion.ObservationAudioTranscript {
		t.Fatalf("combined reader did not preserve visual and audio parts: %#v", bothEvidence)
	}
	both := observeSharedVideoForExperience(t, f, channel, account, thread, "share-visual-and-audio", assetID,
		"[分享来源资料] "+publisher+"\n[实际内容观察] "+bothEvidence.Excerpt,
		bothEvidence.ObservedRange,
		bothEvidence.Excerpt,
		append([]companion.ObservationPart{publisherPart}, bothEvidence.Parts...))

	scope := companion.ScopeID(channel, account, thread)
	candidates, err := f.manager.memory.ListObservedExperienceReflectionCandidates(t.Context(), scope, 12)
	if err != nil {
		t.Fatal(err)
	}
	byEvent := map[string]companion.ObservedExperienceReflectionCandidate{}
	for _, candidate := range candidates {
		byEvent[candidate.SourceEventID] = candidate
	}
	if _, ok := byEvent[card.EventID]; ok {
		t.Fatalf("publisher card became a firsthand experience: %#v", byEvent[card.EventID])
	}
	visualCandidate, ok := byEvent[visual.EventID]
	if !ok {
		t.Fatalf("actual visual observation missing from candidates: %#v", candidates)
	}
	if !strings.Contains(visualCandidate.Summary, visualText) || strings.Contains(visualCandidate.Summary, publisher) || !strings.Contains(visualCandidate.ObservedRange, "视频采样帧") {
		t.Fatalf("visual candidate mixed source classes: %#v", visualCandidate)
	}
	audioCandidate, ok := byEvent[audio.EventID]
	if !ok {
		t.Fatalf("actual audio observation missing from candidates: %#v", candidates)
	}
	if !strings.Contains(audioCandidate.Summary, audioText) || strings.Contains(audioCandidate.Summary, publisher) || !strings.Contains(audioCandidate.ObservedRange, "视频音轨") {
		t.Fatalf("audio candidate mixed source classes: %#v", audioCandidate)
	}
	bothCandidate, ok := byEvent[both.EventID]
	if !ok {
		t.Fatalf("combined visual/audio observation missing from candidates: %#v", candidates)
	}
	if !strings.Contains(bothCandidate.Summary, visualText) || !strings.Contains(bothCandidate.Summary, audioText) || strings.Contains(bothCandidate.Summary, publisher) || !strings.Contains(bothCandidate.ObservedRange, "视频采样帧") || !strings.Contains(bothCandidate.ObservedRange, "视频音轨") {
		t.Fatalf("combined candidate lost or mixed observation evidence: %#v", bothCandidate)
	}

	// A fish-owned reflection remains rooted in the exact source observation.
	// Forgetting it must prevent the same stored video evidence from being
	// offered again by the asynchronous reflection worker.
	item, err := f.manager.memory.SaveExperienceReflection(t.Context(), companion.ExperienceReflectionInput{
		Scope: scope, SourceEventID: visualCandidate.SourceEventID, SourceEventRevision: visualCandidate.SourceEventRevision,
		SourceAssetID: visualCandidate.SourceAssetID, SourceObservationVersion: visualCandidate.SourceObservationVersion,
		SourceObservationHash: visualCandidate.SourceObservationHash, Note: "下次还想看看这种潮汐画面。", Feeling: "平静", WantsToRevisit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.memory.ForgetExperienceReflection(t.Context(), scope, item.ID, item.Revision); err != nil {
		t.Fatal(err)
	}
	candidates, err = f.manager.memory.ListObservedExperienceReflectionCandidates(t.Context(), scope, 12)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.SourceEventID == visual.EventID {
			t.Fatalf("forgotten visual experience re-entered reflection candidates: %#v", candidate)
		}
	}
}

// observeSharedVideoForExperience emulates only the trusted host boundary:
// the original share is retained first, then the reader attaches concrete
// typed coverage to that exact source.  It never calls a model or a network
// service, so it is also a stable replay of delayed video readers.
func observeSharedVideoForExperience(t *testing.T, f *webFixture, channel, account, thread, messageID, assetID, body, observedRange, excerpt string, parts []companion.ObservationPart) companion.SourceRef {
	t.Helper()
	at := time.Now().UTC().Add(time.Duration(len(messageID)) * time.Nanosecond)
	original := "[分享卡片] " + messageID
	source := companion.SourceRef{
		InstanceID: "share-experience-flow", MessageID: messageID, ServerID: messageID,
		Account: account, Thread: thread, Sender: "member-a", SenderName: "群友A",
		MessageType: 49, OccurredAt: at.Format(time.RFC3339Nano), Revision: 1, Integrity: "observed",
	}
	in := companion.Input{
		Channel: channel, Account: account, Thread: thread, IsGroup: true, Sender: source.Sender, SenderName: source.SenderName,
		TraceID: messageID, Text: original, OriginalText: &original, Source: &source, MessageTime: source.OccurredAt,
		IncomingMedia: []companion.IncomingMediaRef{{Source: source, AssetID: assetID, Kind: "share", UploaderID: source.Sender, ObservationState: "pending"}},
	}
	cfg := f.manager.Config().Companion
	cfg.Permissions.Remember = true
	if err := f.manager.memory.Observe(in, cfg, at); err != nil {
		t.Fatal(err)
	}
	ref, ok := f.manager.memory.SourceForInput(in)
	if !ok {
		t.Fatal("stored share source unavailable")
	}
	if err := f.manager.memory.CacheIncomingObservationWithCoverage(channel, *ref, body, observedRange, excerpt, parts...); err != nil {
		t.Fatal(err)
	}
	return *ref
}
