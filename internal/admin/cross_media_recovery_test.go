package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"
)

func crossRecoveryFixture(t *testing.T, parts int) (*webFixture, *permittedTools, crossMediaBatch) {
	t.Helper()
	f, p, _ := forwardFixture(t)
	f.manager.config.Companion.Permissions.SendVoice = true
	uploads := []stagedUpload{}
	for i := 0; i < parts; i++ {
		uploads = append(uploads, stagedUpload{UploadSource: linuxnative.UploadSource{ID: fmt.Sprintf("recovery-part-%d", i), Filename: "part.wav", ContentType: "audio/wav", ByteLength: 15}, Account: "test", Kind: "voice"})
	}
	a, e := f.manager.rememberAssetFormats(p.trace, assetOriginal{VoiceComplete: true, Data: []byte("synthetic-recovery-audio"), MIME: "audio/wav", Filename: "song.wav"}, uploads)
	if e != nil {
		t.Fatal(e)
	}
	task := agentTask{ID: "cross-recovery-parent", Account: "test", Thread: p.trace.Thread, Sender: p.trace.Sender, State: "running", Next: time.Now()}
	if e = f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	p.trace.TaskID = task.ID
	raw, _ := json.Marshal(map[string]string{"asset_id": a.ID, "source_thread_id": p.trace.Thread, "target_thread_id": "source@chatroom", "kind": "voice", "request_key": "recovery"})
	result, e := p.Call(context.Background(), "wechat_send_media", raw)
	if e != nil {
		t.Fatal(e)
	}
	var response struct {
		ID string `json:"operation_id"`
	}
	json.Unmarshal([]byte(result), &response)
	data, e := os.ReadFile(filepath.Join(f.manager.dir, "cross-media", response.ID+".json"))
	var batch crossMediaBatch
	if e != nil || json.Unmarshal(data, &batch) != nil {
		t.Fatal(e)
	}
	return f, p, batch
}

func saveCrossRecoveryBatch(t *testing.T, f *webFixture, b crossMediaBatch) {
	t.Helper()
	data, _ := json.Marshal(b)
	if e := writePrivate(filepath.Join(f.manager.dir, "cross-media", b.ID+".json"), data); e != nil {
		t.Fatal(e)
	}
}

func acceptedCrossPart(b crossMediaBatch, i int) linuxnative.ToolStatus {
	p := b.Parts[i]
	return linuxnative.ToolStatus{ID: "job-" + p.Key, State: "accepted", ThreadID: b.Target, SourceID: p.SourceID, ServerID: fmt.Sprint(800 + i)}
}

func TestCrossMediaLateTaskBindingSettlesAfterReceipt(t *testing.T) {
	f, p, _ := forwardFixture(t)
	f.manager.config.Companion.Permissions.SendVoice = true
	upload := stagedUpload{UploadSource: linuxnative.UploadSource{ID: "late-part", Filename: "part.wav", ContentType: "audio/wav", ByteLength: 15}, Account: "test", Kind: "voice"}
	asset, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{VoiceComplete: true, Data: []byte("synthetic-late-audio"), MIME: "audio/wav", Filename: "song.wav"}, []stagedUpload{upload})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"asset_id": asset.ID, "source_thread_id": p.trace.Thread, "target_thread_id": "source@chatroom", "kind": "voice", "request_key": "late"})
	result, err := p.Call(context.Background(), "wechat_send_media", raw)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	batch, err := f.manager.readCrossBatch(response.ID)
	if err != nil || batch.TaskID != "" {
		t.Fatalf("batch should be unbound: %+v, %v", batch, err)
	}
	parent := agentTask{ID: "late-parent", Account: batch.Account, Thread: batch.Origin, RequestThread: batch.Origin, SourceThread: batch.Source, Sender: batch.Sender, OriginTrace: batch.OriginInput.OriginTraceID, State: "completed", WorkCompleted: true, Next: time.Now()}
	saved, err := f.manager.agent.saveTask(parent)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "waiting_delivery" || !contains(saved.PendingOperations, batch.ID) {
		t.Fatalf("late operation not bound: %+v", saved)
	}
	if err := f.manager.settleCrossMediaPart(batch.ID, batch.Parts[0].Key, acceptedCrossPart(batch, 0)); err != nil {
		t.Fatal(err)
	}
	settled, ok := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
	if !ok || settled.State != "completed" || len(settled.PendingOperations) != 0 {
		t.Fatalf("late operation not settled: %+v, found=%v", settled, ok)
	}
}

func TestCrossMediaWorkerAdmissionUsesCanonicalSourceMetadata(t *testing.T) {
	f, _, batch := crossRecoveryFixture(t, 1)
	seed := batch.Parts[0]
	if !f.manager.crossSeedAllowed(seed) {
		t.Fatal("canonical seed was rejected")
	}
	seed.Media.ID = "" // The worker checkpoint retains the native source ID, not the temporary handle.
	if !f.manager.crossSeedAllowed(seed) {
		t.Fatal("worker admission rejected its own durable batch")
	}
	seed.Media.Title = "different.wav"
	if f.manager.crossSeedAllowed(seed) {
		t.Fatal("worker admission accepted changed media metadata")
	}
}

func TestCrossMediaStatusShowsPreSubmitStopAndCancellation(t *testing.T) {
	f, p, batch := crossRecoveryFixture(t, 1)
	part := batch.Parts[0]
	f.manager.native = &receiptLookupNative{forwardNative: f.manager.native.(*forwardNative), statuses: map[string]linuxnative.ToolStatus{}}
	checkpoint, _ := json.Marshal(map[string]any{"media": []map[string]any{{"key": part.Key, "trace": batch.ID, "input": map[string]string{"thread": batch.Target}, "source_id": part.SourceID, "state": "stopped", "reason": "media_permission_or_quiet_changed"}}})
	if err := os.MkdirAll(f.manager.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(f.manager.stateDir, "messages-v2.json"), checkpoint); err != nil {
		t.Fatal(err)
	}
	status, err := p.crossMediaStatus(context.Background(), batch.ID)
	if err != nil || !strings.Contains(status, `"state":"stopped"`) || !strings.Contains(status, "media_permission_or_quiet_changed") {
		t.Fatalf("pre-submit stop hidden from agent: %s %v", status, err)
	}
	f.login()
	readBatch := func() struct {
		Items []struct {
			State string `json:"state"`
			Parts []struct {
				State string `json:"state"`
			} `json:"parts"`
		} `json:"items"`
	} {
		t.Helper()
		code, body, _ := f.request("GET", "/api/agent/media-batches", nil, true, true)
		var result struct {
			Items []struct {
				State string `json:"state"`
				Parts []struct {
					State string `json:"state"`
				} `json:"parts"`
			} `json:"items"`
		}
		if code != 200 || json.Unmarshal(body, &result) != nil || len(result.Items) != 1 {
			t.Fatalf("batch status unavailable: %d %s", code, body)
		}
		return result
	}
	if current := readBatch(); current.Items[0].State != "stopped" || current.Items[0].Parts[0].State != "stopped" {
		t.Fatalf("pre-submit stop hidden from console: %#v", current.Items[0])
	}
	batch.CancelledAt = time.Now().UTC()
	saveCrossRecoveryBatch(t, f, batch)
	if current := readBatch(); current.Items[0].State != "cancelled" || current.Items[0].Parts[0].State != "cancelled" {
		t.Fatalf("cancelled batch still appears pending: %#v", current.Items[0])
	}
}

func TestHistoricalCrossMediaActivityStaysWithOriginatingMessage(t *testing.T) {
	f, _, batch := crossRecoveryFixture(t, 1)
	origin := batch.OriginInput.OriginTraceID
	items := []LogEntry{
		{Event: "cross_media_queued", ThreadID: batch.Target, MessageID: origin},
		{Event: "media_cancel_requested", ThreadID: batch.Target, MessageID: batch.ID, RelatedMessageID: origin},
		{Event: "message_received", ThreadID: batch.Target, MessageID: "real-target-message"},
	}
	got := (&Server{manager: f.manager}).projectCrossMediaLogOrigins(items)
	for _, index := range []int{0, 1} {
		if got[index].ThreadID != batch.Origin || got[index].MessageID != origin {
			t.Fatalf("synthetic destination round was retained: %#v", got[index])
		}
	}
	if got[2].ThreadID != items[2].ThreadID || got[2].MessageID != items[2].MessageID || items[0].ThreadID != batch.Target {
		t.Fatal("projection changed real target input or original journal", got[2], items[0])
	}
}

type receiptLookupNative struct {
	*forwardNative
	statuses map[string]linuxnative.ToolStatus
	lookups  int
}

func (n *receiptLookupNative) LookupSend(_ context.Context, key string) (linuxnative.ToolStatus, error) {
	n.lookups++
	if s, ok := n.statuses[key]; ok {
		return s, nil
	}
	return linuxnative.ToolStatus{}, linuxnative.ErrToolNotFound
}

func TestStoppedAgentCancelsOnlyUnsubmittedCrossMediaBatch(t *testing.T) {
	f, _, batch := crossRecoveryFixture(t, 1)
	f.login()
	n := &receiptLookupNative{forwardNative: f.manager.native.(*forwardNative), statuses: map[string]linuxnative.ToolStatus{}}
	f.manager.native = n
	if err := f.manager.setConnectionRunning(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	path := "/api/agent/media-batches/" + batch.ID + "/cancel"
	if code, _, _ := f.request("POST", path, map[string]any{}, true, true); code != 200 {
		t.Fatal("unsubmitted batch was not cancelled", code)
	}
	stored, err := f.manager.readCrossBatch(batch.ID)
	if err != nil || stored.CancelledAt.IsZero() || f.manager.crossSeedAllowed(batch.Parts[0]) || len(f.manager.mediaSeeds) != 0 {
		t.Fatal("cancelled batch remains sendable", err, stored.CancelledAt, len(f.manager.mediaSeeds))
	}
	if n.lookups != 1 || n.submits.Load() != 0 {
		t.Fatal("cancellation sent WeChat", n.lookups, n.submits.Load())
	}
	task, ok := f.manager.agent.lookupTask(batch.TaskID, batch.Account, batch.Origin)
	if !ok || len(task.PendingOperations) != 0 || task.State != "stopped" || !task.Next.IsZero() {
		t.Fatalf("cancelled batch left a looping parent task: %#v", task)
	}
}
func TestCrossStatusCommitsObservedReceiptsAndSettlesParentWithoutSending(t *testing.T) {
	f, p, b := crossRecoveryFixture(t, 2)
	n := &receiptLookupNative{forwardNative: f.manager.native.(*forwardNative), statuses: map[string]linuxnative.ToolStatus{}}
	f.manager.native = n
	for i, part := range b.Parts {
		s := acceptedCrossPart(b, i)
		s.State = "running"
		s.ServerID = ""
		s.VoiceChunks = []linuxnative.VoiceChunkStatus{{Index: 0, State: "accepted", HasReceipt: true, ServerID: fmt.Sprint(1000 + i)}}
		n.statuses[part.Key] = s
	}
	task, _ := f.manager.agent.lookupTask(b.TaskID, b.Account, b.Origin)
	task.State = "completed"
	if e := f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	text, e := p.crossMediaStatus(context.Background(), b.ID)
	if e != nil || !strings.Contains(text, `"state":"accepted"`) {
		t.Fatal(text, e)
	}
	saved, e := f.manager.readCrossBatch(b.ID)
	if e != nil || !crossBatchAccepted(saved) || len(f.manager.takeMediaSeeds()) != 0 || len(f.manager.agent.active(b.Account, b.Origin)) != 0 {
		t.Fatal("lookup did not durably settle", saved, e)
	}
	reads := n.lookups
	if _, e = p.crossMediaStatus(context.Background(), b.ID); e != nil || n.lookups != reads {
		t.Fatal("durable receipt queried again", e, n.lookups)
	}
	if n.submits.Load() != 0 {
		t.Fatal("status query sent a message")
	}
}

func TestCrossStatusRejectsWrongTargetBeforeReceiptCommit(t *testing.T) {
	f, p, b := crossRecoveryFixture(t, 1)
	status := acceptedCrossPart(b, 0)
	status.ThreadID = "wrong@chatroom"
	n := &receiptLookupNative{forwardNative: f.manager.native.(*forwardNative), statuses: map[string]linuxnative.ToolStatus{b.Parts[0].Key: status}}
	f.manager.native = n
	if _, e := p.crossMediaStatus(context.Background(), b.ID); e == nil {
		t.Fatal("wrong target accepted")
	}
	saved, e := f.manager.readCrossBatch(b.ID)
	if e != nil || len(saved.Receipts) != 0 || len(f.manager.takeMediaSeeds()) != 1 || n.submits.Load() != 0 {
		t.Fatal("failed lookup mutated delivery", saved, e)
	}
}

func TestCrossMediaNewKeyReusesPendingBatchAndSupersededNeverRestores(t *testing.T) {
	f, p, b := crossRecoveryFixture(t, 3)
	for _, trace := range []string{p.trace.TraceID, "another-owner-message"} {
		p.trace.TraceID = trace
		raw, _ := json.Marshal(map[string]string{"asset_id": b.Asset, "source_thread_id": b.Source, "target_thread_id": b.Target, "kind": "voice", "request_key": "changed-" + trace})
		result, e := p.Call(context.Background(), "wechat_send_media", raw)
		if e != nil || !strings.Contains(result, b.ID) || !strings.Contains(result, "reused_existing_operation") {
			t.Fatal(result, e)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(f.manager.dir, "cross-media"))
	if len(entries) != 1 || len(f.manager.mediaSeeds) != 3 {
		t.Fatal("duplicated pending batch", len(entries), len(f.manager.mediaSeeds))
	}
	b.SupersededBy = agentID("canonical-batch")
	saveCrossRecoveryBatch(t, f, b)
	if e := f.manager.restoreCrossMedia(); e != nil {
		t.Fatal(e)
	}
	if len(f.manager.takeMediaSeeds()) != 0 || f.manager.crossSeedAllowed(b.Parts[0]) {
		t.Fatal("superseded batch restored")
	}
}

func TestCrossRecoveryPrunesAcceptedReceiptBeforeQueueCheckpoint(t *testing.T) {
	f, _, b := crossRecoveryFixture(t, 3)
	b.Receipts = map[string]linuxnative.ToolStatus{b.Parts[0].Key: acceptedCrossPart(b, 0)}
	saveCrossRecoveryBatch(t, f, b)
	// Simulate a crash immediately after receipt journal persistence, before
	// removing the old queue seed. Keep the existing on-disk recovery queue.
	f.manager.mediaSeeds = nil
	if e := f.manager.restoreCrossMedia(); e != nil {
		t.Fatal(e)
	}
	seeds := f.manager.takeMediaSeeds()
	if len(seeds) != 2 {
		t.Fatal("accepted part remained in recovery queue", len(seeds))
	}
	for _, s := range seeds {
		if s.Key == b.Parts[0].Key {
			t.Fatal("accepted part restored")
		}
	}
}

func TestCrossRecoverySettlesCompletedParentAfterReceiptCheckpoint(t *testing.T) {
	f, _, b := crossRecoveryFixture(t, 2)
	task, _ := f.manager.agent.lookupTask(b.TaskID, b.Account, b.Origin)
	task.State = "completed"
	if e := f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	b.Receipts = map[string]linuxnative.ToolStatus{}
	for i, p := range b.Parts {
		b.Receipts[p.Key] = acceptedCrossPart(b, i)
	}
	saveCrossRecoveryBatch(t, f, b)
	f.manager.mediaSeeds = nil
	if e := f.manager.restoreCrossMedia(); e != nil {
		t.Fatal(e)
	}
	if len(f.manager.takeMediaSeeds()) != 0 {
		t.Fatal("fully accepted batch requeued")
	}
	if len(f.manager.agent.active(b.Account, b.Origin)) != 0 {
		t.Fatal("completed parent left waiting after crash")
	}
	archived, ok := f.manager.agent.lookupTask(b.TaskID, b.Account, b.Origin)
	if !ok || archived.State != "completed" || len(archived.PendingOperations) != 0 {
		t.Fatal("verified completion not preserved", archived, ok)
	}
}

func TestCrossRecoveryUsesCanonicalRouteAndPreservesOtherQueuedMedia(t *testing.T) {
	f, _, b := crossRecoveryFixture(t, 2)
	seeds := f.manager.takeMediaSeeds()
	seeds[0].Input.Thread = "wrong-private"
	unrelated := bot.MediaSeed{Key: "unrelated", Trace: "unrelated-trace", SourceID: "unrelated-source"}
	seeds = append([]bot.MediaSeed{unrelated}, seeds...)
	data, _ := json.Marshal(seeds)
	writePrivate(filepath.Join(f.manager.dir, "media-recovery.json"), data)
	f.manager.mediaSeeds = nil
	if e := f.manager.restoreCrossMedia(); e != nil {
		t.Fatal(e)
	}
	restored := f.manager.takeMediaSeeds()
	if len(restored) != 3 || restored[0].Key != unrelated.Key {
		t.Fatal("unrelated queue changed", restored)
	}
	for _, s := range restored[1:] {
		if s.Input.Thread != b.Target {
			t.Fatal("old queue route overrode batch destination")
		}
	}
}

func TestCrossRecoveryRejectsDamagedJournalWithoutStoppingOtherBatches(t *testing.T) {
	for _, kind := range []string{"json", "account", "target", "receipt_source", "extra_receipt"} {
		t.Run(kind, func(t *testing.T) {
			f, _, b := crossRecoveryFixture(t, 2)
			file := filepath.Join(f.manager.dir, "cross-media", b.ID+".json")
			switch kind {
			case "account":
				b.Account = "different-account"
			case "target":
				b.Parts[0].Input.Thread = "wrong-private"
			case "receipt_source":
				s := acceptedCrossPart(b, 0)
				s.SourceID = "wrong-source"
				b.Receipts = map[string]linuxnative.ToolStatus{b.Parts[0].Key: s}
			case "extra_receipt":
				b.Receipts = map[string]linuxnative.ToolStatus{"not-a-part": acceptedCrossPart(b, 0)}
			}
			saveCrossRecoveryBatch(t, f, b)
			if kind == "json" {
				os.WriteFile(file, []byte("broken-json"), 0600)
			}
			before, _ := os.ReadFile(file)
			unrelated := bot.MediaSeed{Key: "unrelated", Trace: "unrelated-trace", SourceID: "unrelated-source"}
			seeds := append(f.manager.takeMediaSeeds(), unrelated)
			data, _ := json.Marshal(seeds)
			writePrivate(filepath.Join(f.manager.dir, "media-recovery.json"), data)
			f.manager.mediaSeeds = nil
			if e := f.manager.restoreCrossMedia(); e != nil {
				t.Fatal("one bad batch prevented startup", e)
			}
			after, _ := os.ReadFile(file)
			if string(before) != string(after) {
				t.Fatal("damaged original was overwritten")
			}
			restored := f.manager.takeMediaSeeds()
			if len(restored) != 1 || restored[0].Key != unrelated.Key {
				t.Fatal("invalid batch adopted or unrelated lost", restored)
			}
			if f.manager.crossSeedAllowed(b.Parts[0]) {
				t.Fatal("invalid batch authorized")
			}
			if _, ok := f.manager.crossPartReceipt(b.ID, b.Parts[0].Key); ok {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}

func TestCrossRecoveryValidatesPersistedRouteAndReceiptBeforeSettlement(t *testing.T) {
	f, _, b := crossRecoveryFixture(t, 1)
	if !f.manager.crossSeedAllowed(b.Parts[0]) {
		t.Fatal("valid route rejected")
	}
	wrong := b.Parts[0]
	wrong.Input.Thread = "wrong-private"
	if f.manager.crossSeedAllowed(wrong) {
		t.Fatal("queue route spoof allowed")
	}
	wrong = b.Parts[0]
	wrong.SourceThread = "different-source"
	if f.manager.crossSeedAllowed(wrong) {
		t.Fatal("source spoof allowed")
	}
	for _, kind := range []string{"target", "source", "missing_id", "unknown"} {
		s := acceptedCrossPart(b, 0)
		switch kind {
		case "target":
			s.ThreadID = "wrong-private"
		case "source":
			s.SourceID = "wrong-source"
		case "missing_id":
			s.ServerID = ""
		case "unknown":
			s.State = "unknown"
		}
		if e := f.manager.settleCrossMediaPart(b.ID, b.Parts[0].Key, s); e == nil {
			t.Fatal("unverified receipt settled", kind)
		}
	}
	if e := f.manager.settleCrossMediaPart(b.ID, b.Parts[0].Key, acceptedCrossPart(b, 0)); e != nil {
		t.Fatal(e)
	}
	if receipt, ok := f.manager.crossPartReceipt(b.ID, b.Parts[0].Key); !ok || receipt.ThreadID != b.Target {
		t.Fatal("verified receipt unavailable", receipt, ok)
	}
}

func TestCrossMediaConsoleShowsOnlyDurableFactsAndDoesNotSend(t *testing.T) {
	f, _, b := crossRecoveryFixture(t, 2)
	if e := f.manager.settleCrossMediaPart(b.ID, b.Parts[0].Key, acceptedCrossPart(b, 0)); e != nil {
		t.Fatal(e)
	}
	if code, _, _ := f.request("GET", "/api/agent/media-batches", nil, true, true); code != 401 {
		t.Fatal("console auth", code)
	}
	f.login()
	code, data, _ := f.request("GET", "/api/agent/media-batches?limit=1", nil, true, true)
	var response struct {
		Items []struct {
			State    string `json:"state"`
			Source   string `json:"source_thread_id"`
			Target   string `json:"target_thread_id"`
			Accepted int    `json:"accepted_parts"`
			Total    int    `json:"total_parts"`
		}
		ReadOnly bool `json:"read_only"`
	}
	if code != 200 || json.Unmarshal(data, &response) != nil || len(response.Items) != 1 {
		t.Fatal(code, string(data))
	}
	row := response.Items[0]
	if row.Source != b.Source || row.Target != b.Target || row.State != "queued" || row.Accepted != 1 || row.Total != 2 || !response.ReadOnly {
		t.Fatal(response)
	}
	if strings.Contains(string(data), b.Parts[0].SourceID) || strings.Contains(string(data), "synthetic-recovery-audio") {
		t.Fatal("internal source or raw data exposed")
	}
	if code, _, _ := f.request("GET", "/api/agent/media-batches?offset=-1", nil, true, true); code != 400 {
		t.Fatal(code)
	}
}

func TestDamagedCrossJournalDoesNotOverwriteHistoryOrPreventManagerStartup(t *testing.T) {
	f := setup(t)
	f.manager.record(bot.Record{Event: "synthetic_prior_history", Category: "system", Status: "observed"})
	directory := filepath.Join(f.manager.dir, "cross-media")
	if e := os.MkdirAll(directory, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(directory, "synthetic-invalid-batch.json")
	if e := os.WriteFile(file, []byte("invalid-journal"), 0600); e != nil {
		t.Fatal(e)
	}
	m, e := NewManager(f.manager.dir, t.TempDir(), f.manager.native)
	if e != nil {
		t.Fatal("damaged batch took manager offline", e)
	}
	defer m.Close()
	seen := map[string]bool{}
	for _, entry := range m.journal {
		seen[entry.Event] = true
	}
	if !seen["synthetic_prior_history"] || !seen["cross_media_recovery_blocked"] {
		t.Fatal("startup lost history or diagnosis", seen)
	}
	data, e := os.ReadFile(file)
	if e != nil || string(data) != "invalid-journal" {
		t.Fatal("original journal changed", e)
	}
}
