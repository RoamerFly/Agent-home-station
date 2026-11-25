package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestWechatFileReadObtainsImagesVoiceAndFilesWithoutObserverPlugin(t *testing.T) {
	_, p, native := forwardFixture(t)
	// The explicit core transport must work while the optional observation/ASR
	// service is absent.  Every case is a same-sender source in the current
	// conversation; no real WeChat send is performed by this test.
	for _, item := range []struct {
		id   string
		typ  int
		body string
	}{
		{"raw-image", 3, ""},
		{"raw-voice", 34, ""},
		{"raw-file", 49, `<msg><appmsg><type>6</type><title>notes.txt</title><appattach><cdnattachurl>synthetic</cdnattachurl></appattach></appmsg></msg>`},
	} {
		native.message = linuxnative.Message{ID: item.id, AccountID: p.trace.Account, ThreadID: p.trace.Thread, SenderID: p.trace.Sender, Type: item.typ, Body: item.body, Direction: "incoming", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		args, _ := json.Marshal(map[string]string{"message_id": item.id})
		result, err := p.callAgent(context.Background(), "wechat_file_read", args)
		if err != nil || !strings.Contains(result, `"status":"original_available"`) || strings.Contains(result, "input_path") || strings.Contains(result, "conversations\\") {
			t.Fatalf("type %d did not use built-in original transport: %s %v", item.typ, result, err)
		}
	}
	if native.downloads != 3 || native.submits.Load() != 0 {
		t.Fatal("core reads unexpectedly used an observer or submitted delivery", native.downloads, native.submits.Load())
	}
}

func TestIncomingOriginalRetainsSourceAndRequiresExplicitVoiceEnrollment(t *testing.T) {
	f, p, native := forwardFixture(t)
	native.message = linuxnative.Message{ID: "voice-original", AccountID: "test", ThreadID: p.trace.Thread, SenderID: p.trace.Sender, Type: 34, Direction: "incoming", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	asset, data, err := (mediaUnderstanding{manager: f.manager}).rememberIncomingOriginal(t.Context(), native.message, "voice", 8<<20)
	if err != nil || string(data) != "synthetic attachment bytes" || asset.ID == "" {
		t.Fatal("raw voice original was not cached", err, asset)
	}
	if asset.FirstRequest == nil || asset.FirstRequest.TraceID != native.message.ID || asset.FirstRequest.RequesterID != p.trace.Sender || asset.FirstRequest.SourceMessageType != 34 || asset.FirstRequest.VoiceSampleConsent {
		t.Fatalf("voice original gained incorrect provenance or consent: %+v", asset.FirstRequest)
	}
	if got, err := f.manager.cachedAssetData(asset); err != nil || string(got) != string(data) {
		t.Fatal("cached raw voice differs", err)
	}
	again, cached, err := (mediaUnderstanding{manager: f.manager}).rememberIncomingOriginal(t.Context(), native.message, "voice", 8<<20)
	if err != nil || again.ID != asset.ID || string(cached) != string(data) || native.downloads != 1 {
		t.Fatal("same incoming original was downloaded again", err, native.downloads)
	}
}

func TestReceivedVoiceReusesSuccessfulTranscriptAndRetainsBothSources(t *testing.T) {
	f, p, native := forwardFixture(t)
	a := mediaUnderstanding{manager: f.manager}
	native.message = linuxnative.Message{ID: "voice-first", AccountID: "test", ThreadID: p.trace.Thread, SenderID: p.trace.Sender, Type: 34, Direction: "incoming", CreatedAt: "2026-11-25T01:00:00Z"}
	first, _, err := a.rememberIncomingOriginal(t.Context(), native.message, "voice", 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	text, cached, err := a.transcribeReceivedVoice(first.Hash, func() (string, error) {
		calls++
		return "synthetic spoken words", nil
	})
	if err != nil || cached || text != "synthetic spoken words" || calls != 1 {
		t.Fatal("first successful transcription was not recorded", text, cached, err, calls)
	}
	path := filepath.Join(f.manager.messageMediaRoot(), "observations", "voices-"+first.Hash+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("successful transcription was not saved in the WeChat workspace", err)
	}

	native.message.ID = "voice-second"
	native.message.CreatedAt = "2026-11-25T01:01:00Z"
	second, _, err := a.rememberIncomingOriginal(t.Context(), native.message, "voice", 8<<20)
	if err != nil || second.ID != first.ID || second.Hash != first.Hash || native.downloads != 2 {
		t.Fatal("second message was not independently read and verified", err, native.downloads)
	}
	if len(second.Requests) != 2 || second.Requests[0].TraceID != "voice-first" || second.Requests[1].TraceID != "voice-second" {
		t.Fatalf("the reused asset lost a message source: %+v", second.Requests)
	}
	text, cached, err = a.transcribeReceivedVoice(second.Hash, func() (string, error) {
		calls++
		return "", errors.New("the expensive recognizer should not run twice")
	})
	if err != nil || !cached || text != "synthetic spoken words" || calls != 1 {
		t.Fatal("same original did not reuse its verified transcript", text, cached, err, calls)
	}

	for _, failure := range []struct {
		hash string
		text string
		err  error
	}{
		{strings.Repeat("a", 64), "", errors.New("recognizer unavailable")},
		{strings.Repeat("b", 64), "", nil},
	} {
		_, _, _ = a.transcribeReceivedVoice(failure.hash, func() (string, error) { return failure.text, failure.err })
		if _, ok := f.manager.readMessageObservation("voices", failure.hash); ok {
			t.Fatal("failed or empty transcription was cached", failure.hash)
		}
	}
}

func TestResolveInboundArtifactDoesNotGuessAnotherMembersAttachment(t *testing.T) {
	f, p, native := forwardFixture(t)
	native.message = linuxnative.Message{ID: "image-original", AccountID: "test", ThreadID: p.trace.Thread, SenderID: p.trace.Sender, Type: 3, Direction: "incoming", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	asset, _, err := (mediaUnderstanding{manager: f.manager}).rememberIncomingOriginal(t.Context(), native.message, "image", 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, msg, err := f.manager.resolveInboundArtifact(t.Context(), p.trace, native.message.ID)
	if err != nil || got.ID != asset.ID || msg.ID != native.message.ID {
		t.Fatal("same sender could not reuse own incoming image", err, got, msg)
	}
	other := p.trace
	other.Sender = "another-member"
	if _, _, err = f.manager.resolveInboundArtifact(t.Context(), other, native.message.ID); err == nil || !strings.Contains(err.Error(), "sender_ambiguous") {
		t.Fatal("unquoted cross-member attachment was selected", err)
	}
	other.QuotedSource = &companion.SourceRef{MessageID: native.message.ID, Sender: native.message.SenderID, Account: native.message.AccountID, Thread: native.message.ThreadID}
	if got, _, err = f.manager.resolveInboundArtifact(t.Context(), other, native.message.ID); err != nil || got.ID != asset.ID {
		t.Fatal("explicit verified quote did not bind original attachment", err, got)
	}
}
