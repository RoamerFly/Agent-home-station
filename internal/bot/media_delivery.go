package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// Native jobs own upload offsets and receipts. Never reconstruct a voice job
// when a receipt is missing: retry the original only if native permits it.
type mediaNative interface {
	Submit(context.Context, linuxnative.ToolSend) (linuxnative.ToolStatus, error)
	LookupSend(context.Context, string) (linuxnative.ToolStatus, error)
	ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error)
	RetryMediaJob(context.Context, string) (linuxnative.ToolStatus, error)
	CancelMediaJob(context.Context, string) (linuxnative.ToolStatus, error)
}
type MediaSeed struct {
	SourceThread string                   `json:"source_thread,omitempty"`
	OriginTask   string                   `json:"origin_task,omitempty"`
	Key          string                   `json:"key,omitempty"`
	OriginThread string                   `json:"origin_thread,omitempty"`
	Requester    string                   `json:"requester,omitempty"`
	Trace        string                   `json:"trace"`
	Input        companion.Input          `json:"input"`
	Media        companion.GeneratedMedia `json:"media"`
	SourceID     string                   `json:"source_id"`
	JobID        string                   `json:"job_id"`
}
type mediaDelivery struct {
	RepairReported bool                   `json:"repair_reported,omitempty"`
	ReconcileAfter time.Time              `json:"reconcile_after,omitempty"`
	CrossSettled   bool                   `json:"cross_settled,omitempty"`
	CrossTask      string                 `json:"cross_task,omitempty"`
	CrossRequester string                 `json:"cross_requester,omitempty"`
	CrossOrigin    string                 `json:"cross_origin,omitempty"`
	CrossSource    string                 `json:"cross_source,omitempty"`
	ParentAssetID  string                 `json:"parent_asset_id,omitempty"`
	CachedMIME     string                 `json:"cached_mime,omitempty"`
	NotBefore      time.Time              `json:"not_before,omitempty"`
	CompletedAt    time.Time              `json:"completed_at,omitempty"`
	DurationMS     int64                  `json:"duration_ms,omitempty"`
	URL            string                 `json:"url,omitempty"`
	Description    string                 `json:"description,omitempty"`
	Key            string                 `json:"key"`
	Trace          string                 `json:"trace"`
	Input          companion.Input        `json:"input"`
	SourceID       string                 `json:"source_id"`
	AssetID        string                 `json:"asset_id,omitempty"`
	Kind           string                 `json:"kind"`
	Title          string                 `json:"title"`
	ID             string                 `json:"id"`
	State          string                 `json:"state"`
	Phase          string                 `json:"phase"`
	Reason         string                 `json:"reason"`
	Retries        int                    `json:"retries"`
	Checks         int                    `json:"checks"`
	UserCancelled  bool                   `json:"user_cancelled,omitempty"`
	Created        time.Time              `json:"created"`
	Next           time.Time              `json:"next"`
	Status         linuxnative.ToolStatus `json:"status"`
	Signature      string                 `json:"signature,omitempty"`
}

func (d *mediaDelivery) terminal() bool {
	return d.State == "accepted" || d.State == "stopped" || d.State == "needs_review"
}
func (w *Worker) draining() bool { return w.config.Draining != nil && w.config.Draining() }
func awaitingMediaReceipt(d *mediaDelivery) bool {
	return (d.State == "waiting" || d.State == "unknown") && (d.Status.State == "needs_review" || d.Status.State == "unknown" || d.Reason == "media_receipt_id_missing")
}

func (w *Worker) mediaPredecessor(index int, d *mediaDelivery) (*mediaDelivery, bool) {
	// Proactive shares can wait behind a real conversation request that has not
	// started uploading. Never interrupt a live native upload for priority.
	if d.Input.Surf {
		for _, other := range w.runtime.Media {
			if other.Input.Thread == d.Input.Thread && !other.Input.Surf && !other.terminal() && other.ID == "" {
				return nil, true
			}
		}
	}
	var previous *mediaDelivery
	for _, part := range w.runtime.Media[:index] {
		if part.Input.Thread != d.Input.Thread {
			continue
		}
		if !part.terminal() {
			// Unknown outcomes remain durable and are reconciled independently;
			// they must not freeze every later unrelated chat forever.
			if awaitingMediaReceipt(part) && part.Trace != d.Trace {
				continue
			}
			if !d.Input.Surf && part.Input.Surf && part.ID == "" && part.Trace != d.Trace {
				continue
			}
			return nil, true
		}
		previous = part
	}
	return previous, false
}
func (w *Worker) mediaTraceOnlyUnknown(trace string) bool {
	found := false
	for _, d := range w.runtime.Media {
		if d.Trace != trace || d.terminal() {
			continue
		}
		if !awaitingMediaReceipt(d) {
			return false
		}
		found = true
	}
	return found
}
func (w *Worker) pendingMedia() bool {
	if w.draining() {
		// Prepared files have no native send intent yet. Keep them on disk for
		// the replacement worker instead of both refusing to send and waiting
		// forever for them to finish. Unknown receipts are parked as before.
		for _, d := range w.runtime.Media {
			if !d.terminal() && !awaitingMediaReceipt(d) && !(d.State == "prepared" && d.ID == "") {
				return true
			}
		}
		return false
	}
	for _, r := range w.runtime.MediaReplies {
		if !r.terminal() {
			if !w.mediaTraceOnlyUnknown(r.Trace) {
				return true
			}
		}
	}
	for _, d := range w.runtime.Media {
		if !d.terminal() && !awaitingMediaReceipt(d) {
			return true
		}
	}
	return false
}
func (w *Worker) mediaAllowed(d *mediaDelivery) bool {
	routeAllowed := w.allowed[d.Input.Thread]
	if d.CrossRequester != "" {
		if w.config.MediaSeedAllowed == nil || !w.config.MediaSeedAllowed(MediaSeed{Key: d.Key, Trace: d.Trace, SourceID: d.SourceID, Input: d.Input, Requester: d.CrossRequester, OriginThread: d.CrossOrigin, SourceThread: d.CrossSource, OriginTask: d.CrossTask}) {
			return false
		}
		// An authenticated owner task may target a known account conversation
		// without enabling ordinary automatic replies in that conversation.
		routeAllowed = true
	}
	c, ok := w.model.(*companion.Engine)
	return ok && routeAllowed && companion.MediaPermitted(c.Config.Permissions, d.Kind) &&
		(d.Input.Mode == "direct" || !c.Config.Quiet(time.Now())) && (!d.Input.Surf || w.config.SurfAllowed == nil || w.config.SurfAllowed(d.Input.Thread))
}
func (w *Worker) queueMedia(ctx context.Context, c *companion.Engine, input companion.Input, m companion.GeneratedMedia) bool {
	_, ok := w.sender.(mediaNative)
	if !ok || w.runtime == nil {
		return false
	}
	source := m.SourceID
	if source == "" {
		source = m.ID
	}
	key := "media-" + messageHash(w.source.Account() + "|" + input.Thread + "|" + input.TraceID + "|" + source + "|" + m.Kind)[:48]
	for _, old := range w.runtime.Media {
		if old.Key == key {
			return true
		}
	}
	if len(w.runtime.Media) >= 1000 {
		kept := w.runtime.Media[:0]
		for _, old := range w.runtime.Media {
			if !old.terminal() || time.Since(old.Created) < 7*24*time.Hour {
				kept = append(kept, old)
			}
		}
		w.runtime.Media = kept
	}
	if len(w.runtime.Media) >= 1000 {
		w.activity(Record{Event: "send_failed", Category: "error", ThreadID: input.Thread, MessageID: input.TraceID, Reason: "media_queue_full", Status: "failed"})
		return true
	}
	d := &mediaDelivery{ParentAssetID: m.ParentAssetID, CachedMIME: m.CachedMIME, DurationMS: m.DurationMS, URL: m.URL, Description: m.Description, Key: key, Trace: input.TraceID, Input: input, SourceID: source, AssetID: m.AssetID, Kind: m.Kind, Title: m.Title, State: "prepared", Created: time.Now().UTC()}
	w.runtime.Media = append(w.runtime.Media, d)
	w.persistErr = w.saveRuntime()
	if w.persistErr != nil {
		return true
	}
	w.mediaRecord(d, "media_queued", "decision")
	w.reconcileMedia(ctx)
	return true
}
func (w *Worker) adoptMediaSeeds() {
	if w.config.MediaSeeds == nil {
		return
	}
	for _, seed := range w.config.MediaSeeds() {
		if seed.Key != "" && (w.config.MediaSeedAllowed == nil || !w.config.MediaSeedAllowed(seed)) {
			continue
		}
		exists := false
		for _, d := range w.runtime.Media {
			if seed.JobID != "" && d.ID == seed.JobID || seed.Key != "" && d.Key == seed.Key {
				exists = true
			}
		}
		if exists || !w.allowed[seed.Input.Thread] && seed.Key == "" {
			continue
		}
		d := &mediaDelivery{ParentAssetID: seed.Media.ParentAssetID, CachedMIME: seed.Media.CachedMIME, URL: seed.Media.URL, Description: seed.Media.Description, DurationMS: seed.Media.DurationMS, Key: "adopt-" + seed.JobID, Trace: seed.Trace, Input: seed.Input, SourceID: seed.SourceID, AssetID: seed.Media.AssetID, Kind: seed.Media.Kind, Title: seed.Media.Title, ID: seed.JobID, State: "queued", Created: time.Now().UTC()}
		if seed.Key != "" {
			d.CrossTask = seed.OriginTask
			d.CrossRequester = seed.Requester
			d.CrossOrigin = seed.OriginThread
			d.CrossSource = seed.SourceThread
			d.Key = seed.Key
			d.State = "prepared"
		}
		w.runtime.Media = append(w.runtime.Media, d)
		w.persistErr = w.saveRuntime()
		if w.persistErr != nil {
			return
		}
	}
}
func (w *Worker) reconcileMedia(ctx context.Context) {
	n, ok := w.sender.(mediaNative)
	if !ok || w.runtime == nil {
		return
	}
	defer func() {
		for _, d := range w.runtime.Media {
			w.refreshMediaParent(d.Trace)
			if d.State == "accepted" && !d.CrossSettled && d.CrossRequester != "" && w.config.MediaSeedSettled != nil {
				if e := w.config.MediaSeedSettled(d.Trace, d.Key, d.Status); e != nil {
					w.activity(Record{Event: "cross_media_receipt_save_failed", Category: "error", ThreadID: d.Input.Thread, MessageID: d.Trace, Reason: "durable_receipt_pending"})
				} else {
					d.CrossSettled = true
					w.persistErr = w.saveRuntime()
				}
			}
		}
	}()
	for index, d := range w.runtime.Media {
		if ctx.Err() != nil || w.persistErr != nil {
			return
		}
		// The receipt journal may have committed just before a crash while this
		// worker checkpoint still says prepared/unknown. Restore the fact before
		// considering a new native lookup, cancellation or send.
		if d.CrossRequester != "" && d.State != "accepted" && w.config.MediaSeedReceipt != nil {
			if receipt, known := w.config.MediaSeedReceipt(d.Trace, d.Key); known && receipt.State == "accepted" && receipt.ServerID != "" && receipt.ThreadID == d.Input.Thread && (receipt.SourceID == "" || receipt.SourceID == d.SourceID) {
				d.State, d.Status, d.Phase = "accepted", receipt, receipt.Phase
				if receipt.ID != "" {
					d.ID = receipt.ID
				}
				if d.CompletedAt.IsZero() {
					d.CompletedAt = time.Now().UTC()
				}
				d.Reason = "cross_media_receipt_restored"
				w.mediaRecord(d, "cross_media_receipt_restored", "decision")
				if w.persistErr != nil {
					return
				}
			}
		}
		if d.terminal() || time.Now().Before(d.Next) {
			continue
		}
		cancelledTask := !w.taskActive(d.Input)
		if cancelledTask && !d.UserCancelled {
			w.cancelMedia(n, d, true, "task_cancelled_before_delivery")
			continue
		}
		if cancelledTask && d.ID == "" {
			d.State = "stopped"
			w.mediaRecord(d, "media_stopped", "decision")
			continue
		}
		if w.draining() && d.State == "prepared" && d.ID == "" {
			continue
		}
		if !cancelledTask && !w.mediaAllowed(d) {
			w.cancelMedia(n, d, false, "media_permission_or_quiet_changed")
			continue
		}
		// Serialize unsent media per conversation. Persist the sampled gap once.
		if d.ID == "" && d.State == "prepared" {
			if d.CrossRequester != "" {
				blocked := false
				for _, part := range w.runtime.Media[:index] {
					if part.Trace == d.Trace && part.CrossRequester != "" && part.State != "accepted" {
						blocked = true
						break
					}
				}
				if blocked {
					continue
				}
			}
			previous, blocked := w.mediaPredecessor(index, d)
			if blocked {
				continue
			}
			if previous != nil {
				if d.CrossRequester != "" && previous.Trace == d.Trace && previous.State != "accepted" {
					continue
				}
				if !previous.terminal() {
					continue
				}
				if d.NotBefore.IsZero() {
					ended := previous.CompletedAt
					if ended.IsZero() {
						ended = previous.Created
					}
					var engine *companion.Engine
					engine, _ = w.model.(*companion.Engine)
					d.NotBefore = ended.Add(w.replyDelay(engine))
					w.persistErr = w.saveRuntime()
					if w.persistErr != nil {
						return
					}
				}
				if time.Now().Before(d.NotBefore) {
					continue
				}
			}
		}
		checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		var status linuxnative.ToolStatus
		var err error
		if d.ID != "" {
			status, err = n.ToolSendStatus(checkCtx, d.ID)
		} else {
			status, err = n.LookupSend(checkCtx, d.Key)
			if errors.Is(err, linuxnative.ErrToolNotFound) {
				if gate := w.sendGate(d.Input.Thread); time.Now().Before(gate) {
					d.Next = gate
					cancel()
					w.persistErr = w.saveRuntime()
					continue
				}
				// Persist before sending; a lost HTTP response is recovered using this
				// same idempotency key, never by creating a different send request.
				d.State = "submitting"
				w.advanceSendGate(d.Input.Thread)
				w.persistErr = w.saveRuntime()
				if w.persistErr == nil {
					status, err = n.Submit(checkCtx, linuxnative.ToolSend{URL: d.URL, Title: d.Title, Description: d.Description, ThreadID: d.Input.Thread, Kind: d.Kind, SourceID: d.SourceID, IdempotencyKey: d.Key})
				}
			}
		}
		cancel()
		d.Next = time.Now().Add(2 * time.Second)
		if err != nil {
			if time.Since(d.Created) > 30*time.Minute && !w.config.Options.RetryUncertain {
				d.State = "needs_review"
				d.Reason = "media_status_unavailable_after_deadline"
			} else {
				d.Reason = "media_receipt_lookup_pending"
				d.Checks++
				d.Next = time.Now().Add(deliveryBackoff(w.config.Options.RetryDelaySeconds, d.Checks))
			}
			w.mediaRecord(d, "media_waiting", "decision")
			continue
		}
		if status.ThreadID != d.Input.Thread || status.SourceID != "" && status.SourceID != d.SourceID {
			d.State = "needs_review"
			d.Reason = "media_identity_mismatch"
			w.mediaRecord(d, "media_stopped", "error")
			continue
		}
		d.ID = status.ID
		d.Status = status
		d.Phase = status.Phase
		status = status.WithVoiceReceipts()
		d.Status = status
		if status.State == "accepted" && status.ServerID != "" {
			d.State = "accepted"
			if d.CompletedAt.IsZero() {
				d.CompletedAt = time.Now().UTC()
				w.advanceSendGate(d.Input.Thread)
			}
			d.Reason = "media_server_accepted"
			w.mediaRecord(d, "message_sent", "sent")
			w.refreshMediaParent(d.Trace)
			continue
		}
		if status.State == "accepted" {
			d.State = "unknown"
			d.Reason = "media_receipt_id_missing"
			w.mediaRecord(d, "media_waiting", "decision")
			continue
		}
		if d.UserCancelled {
			if status.State == "queued" || status.State == "running" {
				w.cancelMedia(n, d, true, "media_cancel_requested_by_user")
			} else {
				d.State = "stopped"
				d.Reason = "media_cancelled_by_user"
				w.mediaRecord(d, "media_stopped", "decision")
				w.refreshMediaParent(d.Trace)
			}
			continue
		}
		switch status.State {
		case "failed", "cancelled":
			d.Reason = status.Reason
			if status.Retryable && w.config.Options.RetryUncertain {
				if d.State != "retry_wait" {
					d.State = "retry_wait"
					d.Checks = 0
					d.Next = time.Now().Add(deliveryBackoff(w.config.Options.RetryDelaySeconds, d.Retries))
				} else {
					d.Checks++
					d.Next = time.Now().Add(15 * time.Second)
					if d.Checks >= 2 {
						d.Retries++
						d.State = "retry_submitting"
						w.persistErr = w.saveRuntime()
						if w.persistErr != nil {
							return
						}
						retryCtx, stop := context.WithTimeout(ctx, 8*time.Second)
						next, e := n.RetryMediaJob(retryCtx, d.ID)
						stop()
						d.State = "queued"
						d.Checks = 0
						d.Reason = "media_retry_same_job_pending"
						if e == nil {
							d.Status = next
							d.Phase = next.Phase
						}
					}
				}
				w.mediaRecord(d, "media_retry_waiting", "decision")
			} else {
				d.State = "stopped"
				if d.Reason == "" {
					d.Reason = "media_retry_not_permitted"
				}
				w.mediaRecord(d, "media_stopped", "error")
				w.refreshMediaParent(d.Trace)
			}
		case "needs_review", "unknown":
			if d.Input.Surf {
				// Continue reading the old receipt, but let explicit user delivery
				// finish before resuming a proactive upload in the same thread.
				if _, blocked := w.mediaPredecessor(index, d); blocked {
					d.State = "waiting"
					d.Next = time.Now().Add(15 * time.Second)
					continue
				}
			}
			if time.Now().Before(d.ReconcileAfter) {
				d.State = "waiting"
				d.Next = time.Now().Add(15 * time.Second)
				continue
			}
			// Reconcile real same-thread messages, then resume the original plan.
			// Explicit retry policy accepts that a delayed sync may hide delivery.
			if retry, ok := w.sender.(interface {
				ReconcileMediaJob(context.Context, string) (linuxnative.ToolStatus, error)
			}); ok && w.config.Options.RetryUncertain && d.Checks > 0 {
				if gate := w.sendGate(d.Input.Thread); time.Now().Before(gate) {
					d.Next = gate
					continue
				}
				w.advanceSendGate(d.Input.Thread)
				retryCtx, stop := context.WithTimeout(ctx, 30*time.Second)
				next, e := retry.ReconcileMediaJob(retryCtx, d.ID)
				stop()
				d.Retries++
				if e == nil {
					d.ReconcileAfter = time.Time{}
					d.Status = next
					d.State = next.State
					d.Phase = next.Phase
					d.Reason = "media_reconciled_original_job_resumed"
					d.Next = time.Now().Add(2 * time.Second)
					w.mediaRecord(d, "media_retry_waiting", "decision")
					continue
				}
				d.Reason = "media_reconciliation_failed_" + status.Reason
				if !d.RepairReported && w.config.MediaRepairRequired != nil {
					input := d.Input
					input.TraceID = d.Trace
					if reportErr := w.config.MediaRepairRequired(input, d.ID, status.Reason); reportErr != nil {
						w.persistErr = reportErr
						return
					}
					d.RepairReported = true
				}
				d.State = "waiting"
				d.Checks++
				d.ReconcileAfter = time.Now().Add(deliveryBackoff(w.config.Options.RetryDelaySeconds, d.Checks))
				d.Next = time.Now().Add(15 * time.Second)
				w.mediaRecord(d, "media_reconciliation_failed", "error")
				continue
			}
			if d.Checks >= 2 && !w.config.Options.RetryUncertain {
				d.State = "needs_review"
				d.Reason = "media_receipt_unverified_native_retry_denied"
			} else {
				d.State = "waiting"
				d.Checks++
				d.Reason = "media_receipt_reconciliation_pending"
				d.ReconcileAfter = time.Now().Add(deliveryBackoff(w.config.Options.RetryDelaySeconds, d.Checks))
				d.Next = time.Now().Add(15 * time.Second)
			}
			w.mediaRecord(d, "media_waiting", "error")
			w.refreshMediaParent(d.Trace)
		default:
			d.State = status.State
			d.Reason = "media_" + status.Phase
			w.mediaRecord(d, "media_sending", "decision")
		}
	}
	w.persistErr = w.saveRuntime()
}
func (w *Worker) mediaRecord(d *mediaDelivery, event, category string) {
	memory := companion.MediaMemory{ParentAssetID: d.ParentAssetID, CachedMIME: d.CachedMIME, URL: d.URL, Key: d.Key, Kind: d.Kind, Title: d.Title, SourceID: d.SourceID, AssetID: d.AssetID, JobID: d.ID, State: d.State, Phase: d.Phase, Reason: d.Reason, Retries: d.Retries, TotalParts: len(d.Status.VoiceChunks)}
	for _, p := range d.Status.VoiceChunks {
		memory.DurationMS += p.DurationMS
		if p.State == "accepted" && p.HasReceipt {
			memory.AcceptedParts++
		}
	}
	if len(d.Status.VoiceChunks) == 0 {
		memory.DurationMS = d.DurationMS
	}
	sig := fmt.Sprintf("%s|%s|%s|%d|%d|%d|%d", d.State, d.Phase, d.Reason, d.Retries, memory.AcceptedParts, memory.TotalParts, memory.DurationMS)
	if sig == d.Signature {
		return
	}
	d.Signature = sig
	if c, ok := w.model.(*companion.Engine); ok {
		input := d.Input
		input.TraceID = d.Trace
		if err := c.Store.ObserveMedia(input, c.Config, memory, time.Now().UTC()); err != nil {
			w.persistErr = err
			return
		}
	}
	content := "[" + d.Kind + "] " + d.Title
	if memory.TotalParts > 0 {
		content += fmt.Sprintf(" · 已接受 %d/%d 段", memory.AcceptedParts, memory.TotalParts)
	}
	w.activity(Record{RelatedMessageID: d.Input.OriginTraceID, Event: event, Category: category, ThreadID: d.Input.Thread, MessageID: d.Trace, Content: content, SendID: d.ID, Status: d.State, Reason: d.Reason, Origin: "bot"})
	if !d.terminal() {
		w.markSending(d.Trace)
	}
	w.persistErr = w.saveRuntime()
}
func (w *Worker) refreshMediaParent(trace string) {
	pending, review, accepted := false, false, false
	for _, p := range w.runtime.Plans {
		if p.Input.TraceID == trace {
			pending = pending || !p.terminal()
			accepted = accepted || len(p.Sent) > 0
		}
	}
	for _, r := range w.runtime.MediaReplies {
		if r.Trace == trace {
			pending = pending || !r.terminal()
		}
	}
	for _, d := range w.runtime.Deliveries {
		if d.Trace == trace && d.Bubble >= 0 {
			pending = pending || d.State != "accepted" && d.State != "stopped"
			accepted = accepted || d.State == "accepted"
		}
	}
	for _, d := range w.runtime.Media {
		if d.Trace == trace {
			pending = pending || !d.terminal()
			review = review || d.State == "needs_review"
			accepted = accepted || d.State == "accepted"
		}
	}
	phase := "stopped"
	if accepted {
		phase = "replied"
	}
	if review {
		phase = "needs_review"
	}
	if pending {
		phase = "sending"
	}
	w.queuePhase(trace, phase)
	w.publishTaskDelivery(trace)
}
func (w *Worker) cancelMedia(n mediaNative, d *mediaDelivery, user bool, reason string) {
	d.UserCancelled = d.UserCancelled || user
	// Persist cancellation intent before the API call so restart never retries it.
	if user {
		d.Reason = reason
		w.persistErr = w.saveRuntime()
		if w.persistErr != nil {
			return
		}
	}
	if d.ID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s, err := n.CancelMediaJob(ctx, d.ID)
		cancel()
		if err == nil {
			d.Status = s
			d.Phase = s.Phase
		}
	}
	d.Reason = reason
	if user || d.ID != "" && d.Status.State != "cancelled" && d.Status.State != "failed" && d.Status.State != "accepted" {
		d.State = "cancelling"
		d.Next = time.Now().Add(time.Second)
	} else {
		d.State = "stopped"
	}
	w.mediaRecord(d, "media_cancel_requested", "decision")
	w.refreshMediaParent(d.Trace)
}
func (w *Worker) stopRequestedVoice(ctx context.Context, m linuxnative.Message) bool {
	created, e := time.Parse(time.RFC3339, m.CreatedAt)
	if e != nil || time.Since(created) > w.maxMessageAge() || created.After(time.Now().Add(30*time.Second)) {
		return false
	}
	if w.runtime == nil || m.Type != 1 || !w.allowed[m.ThreadID] || (!w.addressed(m) && strings.HasSuffix(m.ThreadID, "@chatroom")) {
		return false
	}
	stop := rejectVoice(m.Body)
	replace := replacesVoice(m.Body)
	if !stop && !replace {
		return false
	}
	n, ok := w.sender.(mediaNative)
	if !ok {
		return false
	}
	cancelled := false
	for _, d := range w.runtime.Media {
		if d.Kind == "voice" && d.Input.Thread == m.ThreadID && !d.terminal() && (!replace || stop || d.Input.Sender == "" || d.Input.Sender == m.SenderID) {
			reason := "media_cancel_requested_by_user"
			if replace && !stop {
				reason = "media_superseded_by_new_request"
			}
			w.cancelMedia(n, d, true, reason)
			cancelled = true
		}
	}
	if replace || stop {
		for _, r := range w.runtime.MediaReplies {
			if r.Input.Thread != m.ThreadID || r.terminal() || r.Input.Sender != "" && r.Input.Sender != m.SenderID {
				continue
			}
			voice := false
			for _, d := range w.runtime.Media {
				voice = voice || d.Trace == r.Trace && d.Kind == "voice"
			}
			if voice {
				r.State = "stopped"
				cancelled = true
				w.activity(Record{Event: "media_reply_stopped", Category: "decision", ThreadID: m.ThreadID, MessageID: r.Trace, Reason: "media_superseded_by_new_request", Status: "stopped"})
				w.refreshMediaParent(r.Trace)
			}
		}
	}
	if cancelled {
		w.persistErr = w.saveRuntime()
	}
	return cancelled
}
func replacesVoice(text string) bool {
	text = strings.ToLower(strings.ReplaceAll(text, " ", ""))
	for _, p := range []string{"不要换", "不用换", "别换", "不换了", "不用重新找", "不要重新找"} {
		if strings.Contains(text, p) {
			return false
		}
	}
	for _, p := range []string{"重新找一首", "重新找首", "换一首", "换首歌", "换首", "换歌", "换个歌", "下一首", "别放这首"} {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

func (w *Worker) cancelVoiceForIntent(ctx context.Context, in companion.Input, intent string) {
	if w.runtime == nil || !w.allowed[in.Thread] {
		return
	}
	n, ok := w.sender.(mediaNative)
	if !ok {
		return
	}
	for _, d := range w.runtime.Media {
		if d.Kind == "voice" && d.Input.Thread == in.Thread && !d.terminal() && d.Trace != in.TraceID && (d.Input.Sender == "" || d.Input.Sender == in.Sender) {
			w.cancelMedia(n, d, true, "media_superseded_by_new_request")
		}
	}
	for _, r := range w.runtime.MediaReplies {
		voice := false
		for _, d := range w.runtime.Media {
			voice = voice || d.Trace == r.Trace && d.Kind == "voice"
		}
		if voice && r.Input.Thread == in.Thread && r.Trace != in.TraceID && !r.terminal() && (r.Input.Sender == "" || r.Input.Sender == in.Sender) {
			r.State = "stopped"
			w.activity(Record{Event: "media_reply_stopped", Category: "decision", ThreadID: in.Thread, MessageID: r.Trace, Reason: "media_superseded_by_new_request", Status: "stopped"})
			w.refreshMediaParent(r.Trace)
		}
	}
	w.persistErr = w.saveRuntime()
}
func rejectVoice(text string) bool {
	text = strings.ReplaceAll(text, " ", "")
	for _, phrase := range []string{"不用发语音", "不用语音", "别发语音", "不要发语音", "停止语音", "停下语音", "改发文件", "换成文件"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
func (w *Worker) suspendMedia() {
	if w.runtime == nil || w.draining() && !w.pendingMedia() {
		return
	}
	n, ok := w.sender.(mediaNative)
	if !ok {
		return
	}
	// Leave recoverable state for the next worker; operator pause stops native
	// pacing as well, rather than allowing autonomous sends after the pause.
	deadline := time.Now().Add(10 * time.Second)
	for _, d := range w.runtime.Media {
		if time.Now().After(deadline) {
			break
		}
		if !d.terminal() && d.ID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			status, err := n.CancelMediaJob(ctx, d.ID)
			cancel()
			if err == nil {
				d.Status = status
			}
			d.Next = time.Now().Add(time.Duration(w.config.Options.RetryDelaySeconds) * time.Second)
			d.Reason = "media_worker_suspended"
			w.mediaRecord(d, "media_worker_suspended", "decision")
		}
	}
	w.persistErr = w.saveRuntime()
}
