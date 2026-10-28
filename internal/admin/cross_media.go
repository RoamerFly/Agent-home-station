package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	Source  string `json:"source_thread_id"`
	Target  string `json:"target_thread_id"`
	Asset   string `json:"asset_id"`
	Message string `json:"message_id"`
	Kind    string `json:"kind"`
	Key     string `json:"request_key"`
}
type crossMediaBatch struct {
	TaskID   string                            `json:"task_id,omitempty"`
	Receipts map[string]linuxnative.ToolStatus `json:"receipts,omitempty"`
	ID       string                            `json:"operation_id"`
	Account  string                            `json:"account"`
	Origin   string                            `json:"origin_thread_id"`
	Source   string                            `json:"source_thread_id"`
	Target   string                            `json:"target_thread_id"`
	Sender   string                            `json:"requester_id"`
	Asset    string                            `json:"asset_id"`
	Created  time.Time                         `json:"created"`
	Parts    []bot.MediaSeed                   `json:"parts"`
}

func (p *permittedTools) crossAllowed(source, target string) bool {
	c := p.manager.Config()
	return p.privileged() && p.manager.agent.config().CrossConversation && permittedRecipient(c.Config, source) && permittedRecipient(c.Config, target)
}
func (p *permittedTools) sendMedia(ctx context.Context, args json.RawMessage) (string, error) {
	var in crossMediaInput
	if json.Unmarshal(args, &in) != nil || in.Key == "" || len(in.Key) > 128 || (in.Asset == "") == (in.Message == "") || !contains([]string{"file", "voice", "video", "image"}, in.Kind) {
		return "", toolInputError("provide one real asset_id or message_id, kind and stable request_key")
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
			f, e := m.openAsset(a.Hash)
			if e != nil {
				return nil, e
			}
			defer f.Close()
			h := sha256.New()
			n, e := io.Copy(h, f)
			if e != nil || n != a.Bytes || hex.EncodeToString(h.Sum(nil)) != a.Hash {
				return nil, errors.New("cached_file_integrity_failed")
			}
			if _, e = f.Seek(0, 0); e != nil {
				return nil, e
			}
			native, ok := m.native.(nativeTools)
			if !ok {
				return nil, errors.New("native_upload_unavailable")
			}
			caps, e := native.Capabilities(ctx)
			if e != nil {
				return nil, e
			}
			if limit := caps.MediaSendMaxBytes["file"]; limit > 0 && n > limit {
				return nil, errors.New("native_file_size_limit")
			}
			upload, e := native.Upload(ctx, a.Filename, a.MIME, f, n)
			if e != nil {
				return nil, e
			}
			if e = m.tools.upload(stagedUpload{UploadSource: upload, Account: p.trace.Account, Kind: "file"}); e != nil {
				return nil, e
			}
			outputs = []companion.GeneratedMedia{{ID: a.ID + "-file", SourceID: upload.ID, AssetID: a.ID, Kind: "file", Title: a.Title, CachedMIME: a.MIME}}
		} else {
			raw, e := access.reuseAsset(ctx, in.Asset, in.Kind)
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
		batch := crossMediaBatch{TaskID: p.trace.TaskID, ID: id, Account: p.trace.Account, Origin: p.trace.Thread, Source: in.Source, Target: in.Target, Sender: p.trace.Sender, Asset: in.Asset, Created: time.Now().UTC()}
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
		return map[string]any{"operation_id": id, "state": "queued", "source_thread_id": in.Source, "target_thread_id": in.Target, "asset_id": in.Asset, "parts": len(batch.Parts), "instruction": "附件已排入明确目标会话的持久队列，尚未确认送达。用wechat_operation_status核对。不要把附件再加入当前私聊reply.media_ids，不重复生成或发送。"}, nil
	})
}
func (m *Manager) queueCrossBatch(batch crossMediaBatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.mediaSeeds) == 0 {
		if b, e := os.ReadFile(filepath.Join(m.dir, "media-recovery.json")); e == nil {
			if e = json.Unmarshal(b, &m.mediaSeeds); e != nil {
				return e
			}
		}
	}
	next := append([]bot.MediaSeed{}, m.mediaSeeds...)
	for _, seed := range batch.Parts {
		if status, ok := batch.Receipts[seed.Key]; ok && status.State == "accepted" && status.ServerID != "" && status.ThreadID == batch.Target {
			continue
		}
		found := false
		for _, old := range next {
			found = found || old.Key == seed.Key
		}
		if !found {
			next = append(next, seed)
		}
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
		if json.Unmarshal(data, &batch) != nil || batch.ID != id || batch.Account != m.native.Account() {
			return errors.New("invalid_cross_media_journal")
		}
		batches = append(batches, batch)
	}
	sort.SliceStable(batches, func(i, j int) bool { return batches[i].Created.Before(batches[j].Created) })
	for _, batch := range batches {
		if len(batch.Parts) == 0 || !m.crossSeedAllowed(batch.Parts[0]) {
			continue
		}
		pending := false
		for _, part := range batch.Parts {
			status, ok := batch.Receipts[part.Key]
			pending = pending || !ok || status.State != "accepted" || status.ServerID == "" || status.ThreadID != batch.Target
		}
		if !pending {
			continue
		}
		if batch.TaskID != "" {
			if err = m.bindCrossTask(batch); err != nil {
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
	b, e := os.ReadFile(filepath.Join(p.manager.dir, "cross-media", id+".json"))
	if e != nil {
		return "", e
	}
	var batch crossMediaBatch
	if json.Unmarshal(b, &batch) != nil || batch.Account != p.trace.Account || batch.Origin != p.trace.Thread || batch.Sender != p.trace.Sender || !p.crossAllowed(batch.Source, batch.Target) {
		return "", toolInputError("cross_media_scope_mismatch")
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
	c := m.agent.config()
	source := seed.SourceThread
	if source == "" {
		source = seed.OriginThread
	}
	if seed.OriginTask != "" {
		t, ok := m.agent.lookupTask(seed.OriginTask, m.native.Account(), seed.OriginThread)
		if !ok || t.State == "cancelled" {
			return false
		}
	}
	return c.Enabled && c.CrossConversation && seed.Input.Account == m.native.Account() && contains(c.OwnerIDs, seed.Requester) && c.allowsPrivilegedThread(seed.OriginThread) && permittedRecipient(m.Config().Config, seed.OriginThread) && permittedRecipient(m.Config().Config, source) && permittedRecipient(m.Config().Config, seed.Input.Thread)
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
	if batch.TaskID != "" && len(batch.Receipts) == len(batch.Parts) {
		a := m.agent
		a.mu.Lock()
		t, ok := a.data.Tasks[batch.TaskID]
		if ok && t.Account == batch.Account && t.Thread == batch.Origin {
			pending := []string{}
			for _, op := range t.PendingOperations {
				if op != id {
					pending = append(pending, op)
				}
			}
			t.PendingOperations = pending
			if len(pending) == 0 && t.WorkCompleted && t.State == "waiting_delivery" && (t.Delivery == nil || t.Delivery.State == "accepted") {
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
