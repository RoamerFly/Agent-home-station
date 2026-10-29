package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type crossMediaInput struct {
	Conversion *voiceConversion `json:"conversion,omitempty"`
	Source     string           `json:"source_thread_id"`
	Target     string           `json:"target_thread_id"`
	Asset      string           `json:"asset_id"`
	Message    string           `json:"message_id"`
	Kind       string           `json:"kind"`
	Key        string           `json:"request_key"`
}
type crossMediaBatch struct {
	Conversion      *voiceConversion                  `json:"conversion,omitempty"`
	RequestIdentity string                            `json:"request_identity,omitempty"`
	SupersededBy    string                            `json:"superseded_by,omitempty"`
	TaskID          string                            `json:"task_id,omitempty"`
	Receipts        map[string]linuxnative.ToolStatus `json:"receipts,omitempty"`
	ID              string                            `json:"operation_id"`
	Account         string                            `json:"account"`
	Origin          string                            `json:"origin_thread_id"`
	Source          string                            `json:"source_thread_id"`
	Target          string                            `json:"target_thread_id"`
	Sender          string                            `json:"requester_id"`
	Asset           string                            `json:"asset_id"`
	Created         time.Time                         `json:"created"`
	Parts           []bot.MediaSeed                   `json:"parts"`
}

func validCrossReceipt(batch crossMediaBatch, part bot.MediaSeed, status linuxnative.ToolStatus) bool {
	return status.State == "accepted" && status.ServerID != "" && status.ThreadID == batch.Target && (status.SourceID == "" || status.SourceID == part.SourceID)
}

// The durable batch is the canonical route. A separately checkpointed worker
// queue must never override account, origin, target, or part identity.
func validateCrossBatch(batch crossMediaBatch, account string) error {
	if !validID(batch.ID) || batch.Account != account || batch.Origin == "" || batch.Source == "" || batch.Target == "" || batch.Sender == "" || batch.Asset == "" || len(batch.Parts) == 0 {
		return errors.New("invalid_cross_media_journal")
	}
	if err := batch.Conversion.validate(batch.Parts[0].Media.Kind); err != nil {
		return err
	}
	keys := map[string]bool{}
	for _, p := range batch.Parts {
		source := p.SourceThread
		if source == "" {
			source = p.OriginThread
		}
		if !validID(p.Key) || keys[p.Key] || p.SourceID == "" || p.Trace != batch.ID || p.Input.TraceID != "" && p.Input.TraceID != batch.ID || p.Input.Account != batch.Account || p.Input.Thread != batch.Target || p.OriginThread != batch.Origin || source != batch.Source || p.Requester != batch.Sender || p.OriginTask != batch.TaskID || !contains([]string{"voice", "file", "image", "video"}, p.Media.Kind) {
			return errors.New("cross_media_part_identity_mismatch")
		}
		keys[p.Key] = true
		if status, ok := batch.Receipts[p.Key]; ok && !validCrossReceipt(batch, p, status) {
			return errors.New("cross_media_receipt_identity_mismatch")
		}
	}
	for key := range batch.Receipts {
		if !keys[key] {
			return errors.New("cross_media_receipt_unknown_part")
		}
	}
	return nil
}

func crossBatchAccepted(batch crossMediaBatch) bool {
	if len(batch.Parts) == 0 {
		return false
	}
	for _, part := range batch.Parts {
		status, ok := batch.Receipts[part.Key]
		if !ok || !validCrossReceipt(batch, part, status) {
			return false
		}
	}
	return true
}

func (p *permittedTools) crossAllowed(source, target string) bool {
	return p.privileged() && p.manager.agent.config().CrossConversation && p.manager.accountThreadKnown(source) && p.manager.accountThreadKnown(target)
}
func (p *permittedTools) sendMedia(ctx context.Context, args json.RawMessage) (string, error) {
	var in crossMediaInput
	if json.Unmarshal(args, &in) != nil || in.Key == "" || len(in.Key) > 128 || (in.Asset == "") == (in.Message == "") || !contains([]string{"file", "voice", "video", "image"}, in.Kind) {
		return "", toolInputError("provide one real asset_id or message_id, kind and stable request_key")
	}
	if err := in.Conversion.validate(in.Kind); err != nil {
		return "", err
	}
	if in.Source == "" {
		in.Source = p.trace.Thread
	}
	if in.Target == "" {
		in.Target = p.trace.Thread
	}
	if !p.crossAllowed(in.Source, in.Target) || !companion.MediaPermitted(p.manager.Config().Companion.Permissions, in.Kind) {
		return "", toolInputError("来源、目标或跨会话媒体发送未授权")
	}
	var verified *linuxnative.Message
	if in.Message != "" {
		msg, e := p.manager.native.Message(ctx, in.Message)
		if e != nil || msg.AccountID != p.trace.Account || msg.ThreadID != in.Source || msg.FileAttachmentName() == "" {
			return "", toolInputError("source_message_not_owned_file")
		}
		verified = &msg
	}
	args, _ = json.Marshal(in)
	return p.privilegedCall(ctx, "wechat_send_media", args, func() (any, error) {
		m := p.manager
		m.crossSendMu.Lock()
		defer m.crossSendMu.Unlock()
		identity := p.trace.TraceID
		if p.trace.OriginTraceID != "" {
			identity = p.trace.OriginTraceID
		}
		if p.trace.TaskID != "" {
			identity = "task:" + p.trace.TaskID
		}

		if in.Message != "" {
			msg := *verified
			var e error
			if _, e = (mediaUnderstanding{manager: m}).receiveFile(ctx, msg, msg.FileAttachmentName()); e != nil {
				return nil, e
			}
			m.assetMu.Lock()
			assets, e := m.loadAssets()
			m.assetMu.Unlock()
			if e != nil {
				return nil, e
			}
			for _, a := range assets {
				if a.Account != p.trace.Account || a.Scope != assetScope(companion.Input{Channel: "wechat", Account: p.trace.Account, Thread: in.Source}) {
					continue
				}
				for _, r := range a.Requests {
					if r.TraceID == in.Message {
						in.Asset = a.ID
					}
				}
			}
		}
		if identity != "" && in.Asset != "" {
			entries, e := os.ReadDir(filepath.Join(m.dir, "cross-media"))
			if e != nil && !os.IsNotExist(e) {
				return nil, e
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				old, e := m.readCrossBatch(strings.TrimSuffix(entry.Name(), ".json"))
				if e != nil || old.SupersededBy != "" || old.RequestIdentity != identity && crossBatchAccepted(old) || old.Origin != p.trace.Thread || old.Sender != p.trace.Sender || old.Source != in.Source || old.Target != in.Target || old.Asset != in.Asset || old.Parts[0].Media.Kind != in.Kind || !sameVoiceConversion(old.Conversion, in.Conversion) {
					continue
				}
				state := "queued"
				if crossBatchAccepted(old) {
					state = "accepted"
				}
				return map[string]any{"operation_id": old.ID, "state": state, "source_thread_id": old.Source, "target_thread_id": old.Target, "parts": len(old.Parts), "effective_conversion": old.Conversion.effective(), "reused_existing_operation": true, "instruction": "同一素材目标已有投递批次；核对原operation_id，不创建新的发送队列。"}, nil
			}
		}
		sourceTrace := p.trace
		sourceTrace.Thread = in.Source
		access := &permittedTools{manager: m, trace: sourceTrace, media: map[string]companion.GeneratedMedia{}}
		var outputs []companion.GeneratedMedia
		if in.Kind == "file" {
			m.assetMu.Lock()
			assets, e := m.loadAssets()
			m.assetMu.Unlock()
			if e != nil {
				return nil, e
			}
			var selected *mediaAsset
			for i := range assets {
				a := assets[i]
				if a.ID == in.Asset && a.Account == p.trace.Account && a.Scope == assetScope(sourceTrace) {
					selected = &a
					break
				}
			}
			if selected == nil {
				return nil, errors.New("asset_not_in_source_conversation")
			}
			a := *selected
			if len(a.Hash) != 64 {
				return nil, errors.New("original_file_not_cached")
			}
			n := a.Bytes
			native, ok := m.native.(nativeTools)
			if !ok {
				return nil, errors.New("native_upload_unavailable")
			}
			caps, e := native.Capabilities(ctx)
			if e != nil {
				return nil, e
			}
			if limit := caps.MediaSendMaxBytes["file"]; limit <= 0 || n > limit {
				return nil, errors.New("native_file_size_limit")
			}
			upload, e := m.uploadVerifiedAsset(ctx, native, a)
			if e != nil {
				return nil, e
			}
			if e = m.tools.upload(stagedUpload{UploadSource: upload, Account: p.trace.Account, Kind: "file"}); e != nil {
				return nil, e
			}
			outputs = []companion.GeneratedMedia{{ID: a.ID + "-file", SourceID: upload.ID, AssetID: a.ID, Kind: "file", Title: a.Title, CachedMIME: a.MIME}}
		} else {
			raw, e := access.reuseAssetConverted(ctx, in.Asset, in.Kind, in.Conversion)
			if e != nil {
				return nil, e
			}
			var prepared struct {
				Media []companion.GeneratedMedia `json:"media"`
			}
			if json.Unmarshal([]byte(raw), &prepared) != nil || len(prepared.Media) == 0 {
				return nil, errors.New("requested_media_format_unavailable")
			}
			outputs = prepared.Media
			for i := range outputs {
				if staged, ok := access.media[outputs[i].ID]; ok {
					outputs[i].SourceID = staged.SourceID
				}
				if outputs[i].SourceID == "" {
					outputs[i].SourceID = outputs[i].ID
				}
			}
		}
		if !p.crossAllowed(in.Source, in.Target) {
			return nil, errors.New("cross_media_authorization_revoked")
		}
		id := p.operationID("wechat_send_media", args)
		batch := crossMediaBatch{Conversion: in.Conversion, RequestIdentity: identity, TaskID: p.trace.TaskID, ID: id, Account: p.trace.Account, Origin: p.trace.Thread, Source: in.Source, Target: in.Target, Sender: p.trace.Sender, Asset: in.Asset, Created: time.Now().UTC()}
		for i, media := range outputs {
			input := p.trace
			input.Thread = in.Target
			input.TraceID = id
			input.OriginTraceID = p.trace.TraceID
			input.TaskID = ""
			input.IsGroup = strings.HasSuffix(in.Target, "@chatroom")
			input.QuoteServerID = ""
			key := agentID(id, in.Target, in.Asset, media.SourceID, string(rune(i+1)))
			batch.Parts = append(batch.Parts, bot.MediaSeed{Key: key, Trace: id, Input: input, SourceID: media.SourceID, Media: media, OriginThread: p.trace.Thread, SourceThread: in.Source, Requester: p.trace.Sender, OriginTask: p.trace.TaskID})
		}
		if e := ensureManaged(filepath.Join(m.dir, "cross-media")); e != nil {
			return nil, e
		}
		b, _ := json.Marshal(batch)
		if e := writePrivate(filepath.Join(m.dir, "cross-media", id+".json"), b); e != nil {
			return nil, e
		}
		if batch.TaskID != "" {
			if e := m.bindCrossTask(batch); e != nil {
				return nil, e
			}
		}
		if e := m.queueCrossBatch(batch); e != nil {
			return nil, e
		}
		m.record(bot.Record{Event: "cross_media_queued", Category: "decision", ThreadID: in.Target, Sender: p.trace.Sender, MessageID: p.trace.TraceID, Content: "[" + in.Kind + "] " + outputs[0].Title, Reason: "explicit_destination_persisted", Status: "queued", Origin: "agent"})
		return map[string]any{"operation_id": id, "state": "queued", "source_thread_id": in.Source, "target_thread_id": in.Target, "asset_id": in.Asset, "parts": len(batch.Parts), "effective_conversion": in.Conversion.effective(), "instruction": "附件已排入明确目标会话的持久队列，尚未确认送达。用wechat_operation_status核对。不要把附件再加入当前私聊reply.media_ids，不重复生成或发送。"}, nil
	})
}
func (m *Manager) queueCrossBatch(batch crossMediaBatch) error {
	if e := validateCrossBatch(batch, m.native.Account()); e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.mediaSeeds) == 0 {
		if b, e := os.ReadFile(filepath.Join(m.dir, "media-recovery.json")); e == nil {
			if e = json.Unmarshal(b, &m.mediaSeeds); e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	keys := map[string]bool{}
	for _, seed := range batch.Parts {
		keys[seed.Key] = true
	}
	next := []bot.MediaSeed{}
	for _, old := range m.mediaSeeds {
		if old.Trace != batch.ID && !keys[old.Key] {
			next = append(next, old)
		}
	}
	for _, seed := range batch.Parts {
		if batch.SupersededBy != "" {
			continue
		}
		if status, ok := batch.Receipts[seed.Key]; ok && validCrossReceipt(batch, seed, status) {
			continue
		}
		seed.Input.TraceID = seed.Trace
		seed.Media.SourceID = seed.SourceID
		next = append(next, seed)
	}
	b, e := json.Marshal(next)
	if e != nil {
		return e
	}
	if e = writePrivate(filepath.Join(m.dir, "media-recovery.json"), b); e != nil {
		return e
	}
	m.mediaSeeds = next
	return nil
}

// Repair a crash between the durable batch journal, parent-task binding and
// queue handoff. Only still-authorized original operations are adopted; no
// source conversion, new upload or new send identity is created here.
func (m *Manager) restoreCrossMedia() error {
	entries, err := os.ReadDir(filepath.Join(m.dir, "cross-media"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	batches := []crossMediaBatch{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validID(id) {
			continue
		}
		data, e := os.ReadFile(filepath.Join(m.dir, "cross-media", entry.Name()))
		if e != nil {
			return e
		}
		var batch crossMediaBatch
		if json.Unmarshal(data, &batch) != nil || batch.ID != id {
			if e = m.blockCrossRecovery(id, "invalid_cross_media_journal"); e != nil {
				return e
			}
			continue
		}
		if e = validateCrossBatch(batch, m.native.Account()); e != nil {
			if e = m.blockCrossRecovery(id, e.Error()); e != nil {
				return e
			}
			continue
		}
		batches = append(batches, batch)
	}
	sort.SliceStable(batches, func(i, j int) bool { return batches[i].Created.Before(batches[j].Created) })
	for _, batch := range batches {
		if batch.SupersededBy != "" {
			if err = m.queueCrossBatch(batch); err != nil {
				return err
			}
			if err = m.settleCrossTask(batch); err != nil {
				return err
			}
			continue
		}
		if crossBatchAccepted(batch) {
			// Receipt commit can precede queue and parent-task checkpoints.
			// Complete that handoff without submitting any part again, even if
			// sending permission was subsequently revoked.
			if err = m.queueCrossBatch(batch); err != nil {
				return err
			}
			if err = m.settleCrossTask(batch); err != nil {
				return err
			}
			continue
		}
		if !m.crossSeedAllowed(batch.Parts[0]) {
			continue
		}
		if batch.TaskID != "" {
			if err = m.bindCrossTask(batch); err != nil {
				if err.Error() == "cross_task_scope_mismatch" {
					if err = m.blockCrossRecovery(batch.ID, "cross_task_scope_mismatch"); err != nil {
						return err
					}
					continue
				}
				return err
			}
		}
		if err = m.queueCrossBatch(batch); err != nil {
			return err
		}
	}
	return nil
}
func (p *permittedTools) crossMediaStatus(ctx context.Context, id string) (string, error) {
	if !validID(id) {
		return "", toolInputError("invalid_cross_media_operation_id")
	}
	b, e := os.ReadFile(filepath.Join(p.manager.dir, "cross-media", id+".json"))
	if e != nil {
		return "", e
	}
	var batch crossMediaBatch
	if json.Unmarshal(b, &batch) != nil || batch.Account != p.trace.Account || batch.Origin != p.trace.Thread || batch.Sender != p.trace.Sender || !p.crossAllowed(batch.Source, batch.Target) {
		return "", toolInputError("cross_media_scope_mismatch")
	}
	if e = validateCrossBatch(batch, p.manager.native.Account()); e != nil {
		return "", e
	}
	if batch.SupersededBy != "" {
		out, _ := json.Marshal(map[string]any{"operation_id": batch.ID, "state": "superseded", "superseded_by": batch.SupersededBy, "target_thread_id": batch.Target, "instruction": "已合并到另一个投递批次，检查superseded_by；不要恢复重复发送。"})
		return string(out), nil
	}
	native, ok := p.manager.native.(nativeTools)
	if !ok {
		return "", errors.New("native_status_unavailable")
	}
	states := []linuxnative.ToolStatus{}
	all := true
	for _, part := range batch.Parts {
		status, known := batch.Receipts[part.Key]
		var err error
		if !known {
			status, err = native.LookupSend(ctx, part.Key)
		}
		if errors.Is(err, linuxnative.ErrToolNotFound) {
			status = linuxnative.ToolStatus{State: "prepared", ThreadID: batch.Target}
			all = false
		} else if err != nil {
			return "", err
		}
		if status.ThreadID != batch.Target || status.SourceID != "" && status.SourceID != part.SourceID {
			return "", errors.New("cross_media_receipt_target_mismatch")
		}
		status = status.WithVoiceReceipts()
		if !known && validCrossReceipt(batch, part, status) {
			// This lookup may be the only observer of a late receipt while the
			// worker is disabled or awaiting a different operation. Commit the
			// verified fact before returning it to the Agent.
			if err = p.manager.settleCrossMediaPart(batch.ID, part.Key, status); err != nil {
				return "", err
			}
		}
		all = all && status.State == "accepted" && status.ServerID != ""
		states = append(states, status)
	}
	state := "queued"
	if all {
		state = "accepted"
	}
	out, e := json.Marshal(map[string]any{"operation_id": id, "state": state, "source_thread_id": batch.Source, "target_thread_id": batch.Target, "parts": states})
	return string(out), e
}

func (m *Manager) crossSeedAllowed(seed bot.MediaSeed) bool {
	batch, e := m.readCrossBatch(seed.Trace)
	if e != nil {
		return false
	}
	if batch.SupersededBy != "" {
		return false
	}
	found := false
	source := seed.SourceThread
	if source == "" {
		source = seed.OriginThread
	}
	for _, p := range batch.Parts {
		if p.Key == seed.Key && p.SourceID == seed.SourceID && p.Input.Account == seed.Input.Account && p.Input.Thread == seed.Input.Thread && p.OriginThread == seed.OriginThread && p.Requester == seed.Requester && p.OriginTask == seed.OriginTask && source == batch.Source {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	c := m.agent.config()
	if seed.OriginTask != "" {
		t, ok := m.agent.lookupTask(seed.OriginTask, m.native.Account(), seed.OriginThread)
		if !ok || t.State == "cancelled" {
			return false
		}
	}
	return c.Enabled && c.CrossConversation && seed.Input.Account == m.native.Account() && contains(c.OwnerIDs, seed.Requester) && c.allowsPrivilegedThread(seed.OriginThread) && permittedRecipient(m.Config().Config, seed.OriginThread) && m.accountThreadKnown(source) && m.accountThreadKnown(seed.Input.Thread)
}

func (m *Manager) blockCrossRecovery(id, reason string) error {
	// Preserve the original journal for inspection/repair. Remove only this
	// batch's adoption seeds; worker checkpoints also revalidate against it.
	m.mu.Lock()
	if len(m.mediaSeeds) == 0 {
		data, e := os.ReadFile(filepath.Join(m.dir, "media-recovery.json"))
		if e == nil {
			if e = json.Unmarshal(data, &m.mediaSeeds); e != nil {
				m.mu.Unlock()
				return e
			}
		} else if !os.IsNotExist(e) {
			m.mu.Unlock()
			return e
		}
	}
	remaining := []bot.MediaSeed{}
	for _, seed := range m.mediaSeeds {
		if seed.Trace != id {
			remaining = append(remaining, seed)
		}
	}
	if len(remaining) != len(m.mediaSeeds) {
		data, _ := json.Marshal(remaining)
		if e := writePrivate(filepath.Join(m.dir, "media-recovery.json"), data); e != nil {
			m.mu.Unlock()
			return e
		}
		m.mediaSeeds = remaining
	}
	m.mu.Unlock()
	m.record(bot.Record{Event: "cross_media_recovery_blocked", Category: "error", Status: "needs_review", MessageID: id, Reason: reason, ToolCode: reason, Origin: "agent"})
	return nil
}

func (m *Manager) readCrossBatch(id string) (crossMediaBatch, error) {
	var batch crossMediaBatch
	if !validID(id) {
		return batch, errors.New("invalid_cross_media_identity")
	}
	file := filepath.Join(m.dir, "cross-media", id+".json")
	stat, e := os.Lstat(file)
	if e != nil || !stat.Mode().IsRegular() {
		return batch, errors.New("cross_media_journal_unavailable")
	}
	data, e := os.ReadFile(file)
	if e != nil || json.Unmarshal(data, &batch) != nil || batch.ID != id {
		return batch, errors.New("invalid_cross_media_journal")
	}
	return batch, validateCrossBatch(batch, m.native.Account())
}

func (m *Manager) crossPartReceipt(id, key string) (linuxnative.ToolStatus, bool) {
	batch, e := m.readCrossBatch(id)
	if e != nil {
		return linuxnative.ToolStatus{}, false
	}
	status, ok := batch.Receipts[key]
	return status, ok
}

func (m *Manager) bindCrossTask(batch crossMediaBatch) error {
	a := m.agent
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.data.Tasks[batch.TaskID]
	if !ok || t.Account != batch.Account || t.Thread != batch.Origin || t.Sender != batch.Sender {
		return errors.New("cross_task_scope_mismatch")
	}
	if !contains(t.PendingOperations, batch.ID) {
		t.PendingOperations = append(append([]string{}, t.PendingOperations...), batch.ID)
		a.data.Tasks[t.ID] = t
		return a.persist()
	}
	return nil
}
func (m *Manager) settleCrossMediaPart(id, key string, status linuxnative.ToolStatus) error {
	if !validID(id) || !validID(key) {
		return errors.New("invalid_cross_receipt_identity")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	file := filepath.Join(m.dir, "cross-media", id+".json")
	b, e := os.ReadFile(file)
	if e != nil {
		return e
	}
	var batch crossMediaBatch
	if json.Unmarshal(b, &batch) != nil || batch.Account != m.native.Account() || batch.ID != id {
		return errors.New("cross_batch_account_mismatch")
	}
	if e = validateCrossBatch(batch, m.native.Account()); e != nil {
		return e
	}
	found := false
	for _, part := range batch.Parts {
		if part.Key == key && status.ThreadID == batch.Target && (status.SourceID == "" || status.SourceID == part.SourceID) {
			found = true
		}
	}
	if !found {
		return errors.New("cross_receipt_target_mismatch")
	}
	if status.State != "accepted" || status.ServerID == "" {
		return errors.New("cross_receipt_unverified")
	}
	if batch.Receipts == nil {
		batch.Receipts = map[string]linuxnative.ToolStatus{}
	}
	if old, ok := batch.Receipts[key]; !ok || old.ServerID != status.ServerID {
		batch.Receipts[key] = status
		b, _ = json.Marshal(batch)
		if e = writePrivate(file, b); e != nil {
			return e
		}
	}
	remaining := []bot.MediaSeed{}
	for _, seed := range m.mediaSeeds {
		if seed.Key != key {
			remaining = append(remaining, seed)
		}
	}
	if len(remaining) != len(m.mediaSeeds) {
		b, _ = json.Marshal(remaining)
		if e = writePrivate(filepath.Join(m.dir, "media-recovery.json"), b); e != nil {
			return e
		}
		m.mediaSeeds = remaining
	}
	return m.settleCrossTask(batch)
}

func (m *Manager) settleCrossTask(batch crossMediaBatch) error {
	if batch.TaskID != "" && (crossBatchAccepted(batch) || batch.SupersededBy != "") {
		a := m.agent
		a.mu.Lock()
		t, ok := a.data.Tasks[batch.TaskID]
		var e error
		if ok && t.Account == batch.Account && t.Thread == batch.Origin && t.Sender == batch.Sender {
			pending := []string{}
			for _, op := range t.PendingOperations {
				if op != batch.ID {
					pending = append(pending, op)
				}
			}
			t.PendingOperations = pending
			if batch.SupersededBy == "" && len(pending) == 0 && t.WorkCompleted && t.State == "waiting_delivery" && (t.Delivery == nil || t.Delivery.State == "accepted") {
				t.State = "completed"
			}
			a.data.Tasks[t.ID] = t
			e = a.persist()
		}
		a.mu.Unlock()
		if e != nil {
			return e
		}
		return a.retireSettled()
	}
	return nil
}
