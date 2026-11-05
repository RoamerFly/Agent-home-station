package admin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// TestVoiceEnrollmentCollectQueuesPrivateAuditionFromGroupOrigin exercises the
// normal MCP tool route after a verified preview exists.  It deliberately
// starts in a group, but the only queued media seed is a private delivery to
// the owner.  The original group input remains solely as immutable outbound
// evidence; it must not leak into the media route, quote, or target.
func TestVoiceEnrollmentCollectQueuesPrivateAuditionFromGroupOrigin(t *testing.T) {
	f, p, _, _, _ := namedSpeechContractFixture(t)
	native := &forwardNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native

	f.manager.mu.Lock()
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "family@chatroom", Mode: "mention"})
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendVoice = true
	f.manager.mu.Unlock()
	settings := f.manager.agent.config()
	settings.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}

	text := "用这个建立我的声线，先给我试听"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	group := companion.Input{
		Channel: "wechat", Account: "test", Thread: "family@chatroom", Sender: "wxid_owner",
		TraceID: "voice-preview-request", OriginTraceID: "voice-preview-request", IsGroup: true,
		Text: text, OriginalText: &text, MessageTime: now,
		Source: &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: "voice-preview-request", ServerID: "synthetic-owner-preview-request", Account: "test", Thread: "family@chatroom", Sender: "wxid_owner", MessageType: 1, OccurredAt: now, Revision: 1, Integrity: "verified"},
	}
	if err := f.manager.memory.Observe(group, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	var ok bool
	if group.Source, ok = f.manager.memory.SourceForInput(group); !ok {
		t.Fatal("missing durable group request source")
	}
	// The snapshot is minted before the asynchronous preview runs.  This also
	// validates that a later private delivery cannot replace the group evidence.
	scope := &permittedTools{manager: f.manager}
	scope.SetTrace(group)
	proof, err := scope.prepareCrossMediaOrigin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	group.ReplyContext = proof.ReplyContext
	p.SetTrace(group)

	profileID := "ve-" + agentID(group.Account, group.Sender, "我的声音")[:24]
	const jobID = "vjob-private-preview"
	preview := syntheticNamedSpeechWAV()
	previewSHA := namedSpeechSHA(preview)
	if err := ensureManaged(f.manager.enrollDir(profileID)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(f.manager.enrollDir(profileID), jobID+".wav"), preview); err != nil {
		t.Fatal(err)
	}
	profile := enrollmentProfile{ID: profileID, Name: "我的声音", Owner: "wxid_owner", Revision: 1, Jobs: []enrollmentJob{{
		ID: jobID, Action: "preview", Revision: 1, State: "reference_preview_ready", Text: "这是新的试听句子。", Origin: group, Preview: previewSHA,
	}}}
	if err := f.manager.saveEnrollment(profile); err != nil {
		t.Fatal(err)
	}

	args := json.RawMessage(`{"action":"collect","name":"我的声音","job_id":"vjob-private-preview"}`)
	result, err := p.Call(context.Background(), "voice_enroll", args)
	if err != nil {
		t.Fatal(err)
	}
	var queued struct {
		ID     string `json:"operation_id"`
		State  string `json:"state"`
		Target string `json:"target_thread_id"`
	}
	if err := json.Unmarshal([]byte(result), &queued); err != nil || queued.ID == "" || queued.State != "queued" || queued.Target != "wxid_owner" {
		t.Fatalf("unexpected private audition result: %s (%v)", result, err)
	}
	seeds := f.manager.takeMediaSeeds()
	if len(seeds) != 1 || native.submits.Load() != 0 {
		t.Fatalf("preview was submitted or did not enter the durable queue: seeds=%d submits=%d", len(seeds), native.submits.Load())
	}
	seed := seeds[0]
	if seed.Input.Thread != "wxid_owner" || seed.Input.IsGroup || seed.Input.QuoteServerID != "" || seed.OriginThread != "family@chatroom" || seed.SourceThread != "wxid_owner" || seed.Requester != "wxid_owner" || seed.Media.Kind != "voice" {
		t.Fatalf("group origin leaked into private audition route: %#v", seed)
	}
	batch, err := f.manager.readCrossBatch(queued.ID)
	if err != nil || batch.OriginInput == nil || batch.OriginInput.Thread != "family@chatroom" || batch.Target != "wxid_owner" || !f.manager.crossSeedAllowed(seed) {
		t.Fatalf("private route lost its verified source snapshot: batch=%#v err=%v", batch, err)
	}

	// Repeating collect is an idempotent status read: it must reuse this batch,
	// not create a second media seed or submit a real WeChat message.
	again, err := p.Call(context.Background(), "voice_enroll", args)
	if err != nil || !json.Valid([]byte(again)) || len(f.manager.takeMediaSeeds()) != 1 || native.submits.Load() != 0 {
		t.Fatalf("collect replay duplicated private delivery: result=%s err=%v seeds=%d submits=%d", again, err, len(f.manager.takeMediaSeeds()), native.submits.Load())
	}

	// The same normal tool route cancels an unsubmitted audition: it removes the
	// persistent seed rather than merely hiding the preview in the UI.
	cancelArgs := json.RawMessage(`{"action":"cancel","name":"我的声音","job_id":"vjob-private-preview"}`)
	if _, err = p.Call(context.Background(), "voice_enroll", cancelArgs); err != nil {
		t.Fatal(err)
	}
	cancelled, err := f.manager.readCrossBatch(queued.ID)
	if err != nil || cancelled.CancelledAt.IsZero() || len(f.manager.takeMediaSeeds()) != 0 || f.manager.crossSeedAllowed(seed) {
		t.Fatalf("cancelled audition remains deliverable: batch=%#v err=%v seeds=%d", cancelled, err, len(f.manager.takeMediaSeeds()))
	}
	if _, err = p.Call(context.Background(), "voice_enroll", args); err == nil || len(f.manager.takeMediaSeeds()) != 0 || native.submits.Load() != 0 {
		t.Fatalf("cancelled preview was requeued: err=%v seeds=%d submits=%d", err, len(f.manager.takeMediaSeeds()), native.submits.Load())
	}

	// A separate preview follows the normal queue-to-native-receipt path.  The
	// first cancelled request cannot be mistaken for this second idempotency key.
	const receiptJob = "vjob-private-receipt"
	if err = writePrivate(filepath.Join(f.manager.enrollDir(profileID), receiptJob+".wav"), preview); err != nil {
		t.Fatal(err)
	}
	profile, err = f.manager.loadEnrollment(profileID)
	if err != nil {
		t.Fatal(err)
	}
	profile.Jobs = append(profile.Jobs, enrollmentJob{ID: receiptJob, Action: "preview", Revision: 1, State: "reference_preview_ready", Text: "另一句新的试听。", Origin: group, Preview: previewSHA})
	if err = f.manager.saveEnrollment(profile); err != nil {
		t.Fatal(err)
	}
	receiptArgs := json.RawMessage(`{"action":"collect","name":"我的声音","job_id":"vjob-private-receipt"}`)
	receiptResult, err := p.Call(context.Background(), "voice_enroll", receiptArgs)
	if err != nil {
		t.Fatal(err)
	}
	var receiptQueued struct {
		ID string `json:"operation_id"`
	}
	if json.Unmarshal([]byte(receiptResult), &receiptQueued) != nil || receiptQueued.ID == "" {
		t.Fatalf("missing receipt operation: %s", receiptResult)
	}
	queuedSeeds := f.manager.takeMediaSeeds()
	if len(queuedSeeds) != 1 {
		t.Fatalf("expected one independently queued audition, got %d", len(queuedSeeds))
	}
	if _, err = native.Submit(context.Background(), linuxnative.ToolSend{ThreadID: queuedSeeds[0].Input.Thread, Kind: "voice", SourceID: queuedSeeds[0].SourceID, IdempotencyKey: queuedSeeds[0].Key}); err != nil {
		t.Fatal(err)
	}
	statusArgs, _ := json.Marshal(map[string]string{"operation_id": receiptQueued.ID})
	status, err := p.Call(context.Background(), "wechat_operation_status", statusArgs)
	if err != nil || !strings.Contains(status, `"state":"accepted"`) || len(f.manager.takeMediaSeeds()) != 0 {
		t.Fatalf("private audition receipt was not recorded: status=%s err=%v seeds=%d", status, err, len(f.manager.takeMediaSeeds()))
	}
}
