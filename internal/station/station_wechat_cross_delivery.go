package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	wechatapp "wechat-bot/connected-apps/wechat/adapter"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

type stationWechatCrossDeliveryPart struct {
	Key            string                 `json:"key"`
	Text           string                 `json:"text"`
	Attempted      bool                   `json:"attempted"`
	BodyUnverified bool                   `json:"body_unverified,omitempty"`
	Status         linuxnative.ToolStatus `json:"status"`
	Upload         string                 `json:"upload_id,omitempty"`
}
type stationWechatCrossDelivery struct {
	Version  int                              `json:"version"`
	Key      string                           `json:"key"`
	Account  string                           `json:"account_id"`
	Origin   string                           `json:"origin_thread"`
	Trace    string                           `json:"origin_message"`
	Target   string                           `json:"target_thread"`
	Created  time.Time                        `json:"created_at"`
	Proofs   []stationWechatCrossProof        `json:"proofs"`
	Parts    []stationWechatCrossDeliveryPart `json:"parts"`
	Original *wechatapp.AttachmentSource      `json:"original_file,omitempty"`
}

func stationCrossReceiptKey(key string) bool {
	return strings.HasPrefix(key, "cross-reply-") || strings.HasPrefix(key, "cross-forward-")
}

func openWechatCrossDelivery(worker *stationWechatWorker, create bool) (*os.Root, error) {
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if create {
		return workspacefs.EnsureDirectory(root, "cross-memory-deliveries")
	}
	return workspacefs.OpenExistingDirectory(root, "cross-memory-deliveries")
}
func readWechatCrossDelivery(root *os.Root, key, account string) (stationWechatCrossDelivery, error) {
	var row stationWechatCrossDelivery
	if !stationCrossReceiptKey(key) || !identity.ValidPathID(key) {
		return row, apphost.ErrNotFound
	}
	raw, err := readLegacyImportFile(root, key+".json", 1<<20)
	if err != nil {
		return row, err
	}
	if json.Unmarshal(raw, &row) != nil || row.Version != 1 || row.Key != key || row.Account != account || !identity.ValidPathID(row.Target) || !identity.ValidPathID(row.Origin) || row.Trace == "" || row.Created.IsZero() || len(row.Parts) == 0 || len(row.Proofs) == 0 {
		return stationWechatCrossDelivery{}, apphost.ErrUnavailable
	}
	forward := strings.HasPrefix(key, "cross-forward-")
	if forward != (row.Original != nil) || forward && (len(row.Parts) != 1 || len(row.Proofs) != 1 || row.Original.Kind != "file" || row.Original.Account != account || row.Original.Thread != row.Proofs[0].Thread || row.Original.Thread == row.Target || row.Origin != row.Target) {
		return stationWechatCrossDelivery{}, apphost.ErrUnavailable
	}
	for i, p := range row.Parts {
		if p.Key != key+"-"+fmt.Sprint(i) || !forward && (strings.TrimSpace(p.Text) == "" || len(p.Text) > 3000 || p.Upload != "") || forward && (p.Text != "" || p.Attempted && !identity.ValidPathID(p.Upload)) {
			return stationWechatCrossDelivery{}, apphost.ErrUnavailable
		}
		if p.Status.ID != "" {
			row.Parts[i].Status.AccountID = account
		}
	}
	return row, nil
}

func (row stationWechatCrossDelivery) matchesStatus(part stationWechatCrossDeliveryPart, status linuxnative.ToolStatus) bool {
	if status.AccountID != row.Account {
		return false
	}
	if row.Original == nil {
		return validCrossDeliveryStatus(status, row.Target)
	}
	return status.ID != "" && status.ThreadID == row.Target && status.SourceID == part.Upload && part.Upload != "" && (status.Kind == "file" || status.Kind == "send" || status.Kind == "")
}

func (row stationWechatCrossDelivery) matchesNative(ctx context.Context, native *linuxnative.Sender) bool {
	if native == nil || native.Account() != row.Account {
		return false
	}
	if row.Original == nil {
		return true
	}
	dataset, err := native.Identity(ctx)
	return err == nil && dataset == row.Original.Dataset
}
func saveWechatCrossDelivery(root *os.Root, row stationWechatCrossDelivery) error {
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	if len(data) >= 1<<20 {
		return errors.New("私聊投递记录超过本机单文件容量，请拆分为多个独立请求")
	}
	temp := ".cross-send-" + identity.RandomToken()
	defer root.Remove(temp)
	if err := writeLegacyStagedFile(root, temp, row); err != nil {
		return err
	}
	return root.Rename(temp, row.Key+".json")
}
func validCrossDeliveryStatus(status linuxnative.ToolStatus, target string) bool {
	return status.ID != "" && status.ThreadID == target && (status.Kind == "text" || status.Kind == "send" || status.Kind == "") && status.SourceID == "" && status.AccountID != ""
}

// The entire original batch is frozen before any native submission. Once a
// part is marked attempted, retries only look up that key, even when the
// transport response was lost or a later model drafts different wording.
func (t *stationAttachmentTools) RoutePrivateReply(ctx context.Context, messages []string) error {
	if !t.PrivateReplyRequired() || t.PrivateReplyTarget() == "" || len(t.cross.Proofs) == 0 {
		return errors.New("cross_memory_private_route_unavailable")
	}
	if err := t.checkCrossResults(ctx); err != nil {
		return err
	}
	w, err := t.crossHost.worker(t.crossConnection, false)
	if err != nil || w == nil {
		return apphost.ErrUnavailable
	}
	w.crossDeliveryMu.Lock()
	defer w.crossDeliveryMu.Unlock()
	root, err := openWechatCrossDelivery(w, true)
	if err != nil {
		return err
	}
	defer root.Close()
	key := "cross-reply-" + localCheckpointKey(t.store.SourceInstanceID(), t.trace.Account, t.trace.Thread, t.trace.TraceID, t.cross.Target)[:40]
	row, err := readWechatCrossDelivery(root, key, t.trace.Account)
	if errors.Is(err, os.ErrNotExist) {
		row = stationWechatCrossDelivery{Version: 1, Key: key, Account: t.trace.Account, Origin: t.trace.Thread, Trace: t.trace.TraceID, Target: t.cross.Target, Created: time.Now().UTC(), Proofs: append([]stationWechatCrossProof(nil), t.cross.Proofs...)}
		for _, message := range messages {
			text := strings.TrimSpace(message)
			if text == "" || len(text) > 3000 {
				return errors.New("cross_memory_private_text_invalid")
			}
			row.Parts = append(row.Parts, stationWechatCrossDeliveryPart{Key: key + "-" + fmt.Sprint(len(row.Parts)), Text: text})
		}
		if len(row.Parts) == 0 {
			return errors.New("cross_memory_private_text_missing")
		}
		if err := saveWechatCrossDelivery(root, row); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if row.Origin != t.trace.Thread || row.Trace != t.trace.TraceID || row.Target != t.cross.Target {
		return apphost.ErrObservationForbidden
	}
	for i := range row.Parts {
		for _, proof := range row.Proofs {
			if _, err := t.crossProof(ctx, proof); err != nil {
				return err
			}
		}
		part := &row.Parts[i]
		status, lookupErr := t.native.LookupSend(ctx, part.Key)
		if part.Attempted {
			if errors.Is(lookupErr, linuxnative.ErrToolNotFound) && part.Status.State == "accepted" && part.Status.ServerID != "" && validCrossDeliveryStatus(part.Status, row.Target) {
				continue
			}
			if lookupErr != nil {
				return errors.New("cross_memory_private_receipt_unknown")
			}
		} else if lookupErr == nil {
			// A receipt without a matching local claim cannot establish the
			// newly generated body. Preserve evidence and never submit it again.
			part.Attempted, part.BodyUnverified = true, true
			part.Status = status
			if err := saveWechatCrossDelivery(root, row); err != nil {
				return err
			}
			return errors.New("cross_memory_private_original_body_unverified")
		} else {
			if !errors.Is(lookupErr, linuxnative.ErrToolNotFound) {
				return lookupErr
			}
			part.Attempted = true
			if err := saveWechatCrossDelivery(root, row); err != nil {
				return err
			}
			if err := w.withDeliverySubmit(ctx, t.trace, func() error {
				if err := t.checkCrossResults(ctx); err != nil {
					return err
				}
				for _, proof := range row.Proofs {
					if _, err := t.crossProof(ctx, proof); err != nil {
						return err
					}
				}
				var sendErr error
				status, sendErr = t.native.Submit(ctx, linuxnative.ToolSend{ThreadID: row.Target, Kind: "text", Text: part.Text, IdempotencyKey: part.Key})
				return sendErr
			}); err != nil {
				return err
			}
		}
		if !validCrossDeliveryStatus(status, row.Target) || status.AccountID != row.Account {
			return errors.New("cross_memory_private_receipt_mismatch")
		}
		part.Status = status
		if err := saveWechatCrossDelivery(root, row); err != nil {
			return err
		}
		if part.BodyUnverified {
			return errors.New("cross_memory_private_original_body_unverified")
		}
		switch status.State {
		case "accepted":
			if status.ServerID == "" {
				return errors.New("cross_memory_private_acceptance_unverified")
			}
		case "queued", "running", "submitting", "pending":
		case "failed", "cancelled", "rejected", "needs_review":
			return errors.New("cross_memory_private_delivery_failed")
		default:
			return errors.New("cross_memory_private_receipt_unknown")
		}
	}
	return nil
}

// Receipt lookup remains available after source revocation, without opening
// memory, replaying text or admitting another native write.
func (s *stationWechat) readCrossMemoryReceipt(ctx context.Context, id, key string) (apphost.Receipt, error) {
	if !stationCrossReceiptKey(key) || !identity.ValidPathID(key) {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	w, err := s.worker(id, false)
	if err != nil || w == nil {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	w.crossDeliveryMu.Lock()
	defer w.crossDeliveryMu.Unlock()
	root, err := openWechatCrossDelivery(w, false)
	if err != nil {
		return apphost.Receipt{}, err
	}
	defer root.Close()
	native, nativeErr := s.WorkerSender(ctx, id)
	account := ""
	if nativeErr == nil {
		account = native.Account()
	} else {
		w.ownerMu.Lock()
		data, readErr := readLegacyImportFile(root, key+".json", 1<<20)
		w.ownerMu.Unlock()
		var header struct {
			Account string `json:"account_id"`
		}
		if readErr != nil || json.Unmarshal(data, &header) != nil {
			return apphost.Receipt{}, apphost.ErrNotFound
		}
		account = header.Account
	}
	if wechatapp.ConnectionID(account) != id {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	row, err := readWechatCrossDelivery(root, key, account)
	if err != nil {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	changed := false
	if nativeErr == nil && row.matchesNative(ctx, native) {
		for i := range row.Parts {
			part := &row.Parts[i]
			if !part.Attempted {
				continue
			}
			status, err := native.LookupSend(ctx, part.Key)
			if err == nil && row.matchesStatus(*part, status) {
				part.Status = status
				changed = true
			}
		}
	}
	if changed {
		if err := saveWechatCrossDelivery(root, row); err != nil {
			return apphost.Receipt{}, err
		}
	}
	out := s.crossMemoryReceipt(id, row)
	out.CancelRequested, err = w.deliveryCancelled(companion.Input{Account: row.Account, Thread: row.Origin, TraceID: row.Trace})
	return out, err
}

func (s *stationWechat) crossMemoryReceipt(id string, row stationWechatCrossDelivery) apphost.Receipt {
	kind, lookup := "text", "host_private_route_checkpoint"
	if row.Original != nil {
		kind, lookup = "file", "host_original_forward_checkpoint"
	}
	out := apphost.Receipt{ConnectionRef: s.connectionRef(id, row.Account), ConversationID: row.Target, DeliveryReceipt: apphost.DeliveryReceipt{IdempotencyKey: row.Key, Kind: kind, State: "accepted", Lookup: lookup}}
	for i, part := range row.Parts {
		p := apphost.DeliveryReceipt{IdempotencyKey: part.Key, OperationID: part.Status.ID, Kind: kind, State: part.Status.State, PlatformState: part.Status.State, ServerID: part.Status.ServerID, Lookup: lookup}
		if p.State == "" {
			p.State = "unknown"
		}
		out.Deliveries = append(out.Deliveries, p)
		if p.State == "accepted" && p.ServerID != "" && !part.BodyUnverified {
			out.AcceptedParts = append(out.AcceptedParts, apphost.ReceiptPart{Index: i, ServerID: p.ServerID, State: p.State})
		} else {
			out.State = "pending"
			out.UnconfirmedParts = append(out.UnconfirmedParts, apphost.ReceiptPart{Index: i, State: p.State})
		}
	}
	if row.Original != nil && len(out.UnconfirmedParts) == 1 {
		out.State = out.UnconfirmedParts[0].State
		if !row.Parts[0].Attempted {
			out.State = "prepared"
		}
		if out.State == "accepted" {
			out.State = "unknown"
		}
	}
	if row.Original != nil {
		out.OperationID = row.Parts[0].Status.ID
		if out.State == "accepted" {
			out.ServerID = row.Parts[0].Status.ServerID
		}
	}
	return out
}

const stationCrossReceiptStart = "cross-reply-start"

func (s *stationWechat) listCrossMemoryReceipts(ctx context.Context, id, after string, limit int) (apphost.ReceiptPage, error) {
	page := apphost.ReceiptPage{Items: []apphost.Receipt{}}
	w, err := s.worker(id, false)
	if err != nil {
		return page, err
	}
	if w == nil {
		return page, nil
	}
	w.crossDeliveryMu.Lock()
	defer w.crossDeliveryMu.Unlock()
	root, err := openWechatCrossDelivery(w, false)
	if errors.Is(err, os.ErrNotExist) {
		return page, nil
	}
	if err != nil {
		return page, err
	}
	defer root.Close()
	file, err := root.Open(".")
	if err != nil {
		return page, err
	}
	entries, err := file.ReadDir(-1)
	file.Close()
	if err != nil {
		return page, err
	}
	keys := []string{}
	for _, entry := range entries {
		key := strings.TrimSuffix(entry.Name(), ".json")
		if !entry.IsDir() && stationCrossReceiptKey(key) && strings.HasSuffix(entry.Name(), ".json") && identity.ValidPathID(key) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if after == stationCrossReceiptStart {
		after = ""
	}
	for _, key := range keys {
		if key <= after {
			continue
		}
		if ctx.Err() != nil {
			return page, ctx.Err()
		}
		if len(page.Items) == limit {
			page.NextAfter = page.Items[len(page.Items)-1].IdempotencyKey
			break
		}
		raw, err := readLegacyImportFile(root, key+".json", 1<<20)
		if err != nil {
			return page, apphost.ErrUnavailable
		}
		var header struct {
			Account string `json:"account_id"`
		}
		if json.Unmarshal(raw, &header) != nil || wechatapp.ConnectionID(header.Account) != id {
			return page, apphost.ErrUnavailable
		}
		row, err := readWechatCrossDelivery(root, key, header.Account)
		if err != nil {
			return page, err
		}
		receipt := s.crossMemoryReceipt(id, row)
		receipt.CancelRequested, err = w.deliveryCancelled(companion.Input{Account: row.Account, Thread: row.Origin, TraceID: row.Trace})
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, receipt)
	}
	page.Snapshot = localCheckpointKey(keys, page.Items)
	page.Coverage = "host_private_cross_memory_checkpoints"
	return page, nil
}

func (s *stationWechat) cancelCrossMemoryDelivery(w http.ResponseWriter, r *http.Request, id, key string) {
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		websession.Fail(w, 404, "私聊投递记录不存在")
		return
	}
	root, err := openWechatCrossDelivery(worker, false)
	if err != nil {
		websession.Fail(w, 404, "私聊投递记录不存在")
		return
	}
	defer root.Close()
	// Read only the immutable original identity before waiting for deliveryMu.
	// A sender can hold crossDeliveryMu while it is entering the native queue.
	raw, err := readLegacyImportFile(root, key+".json", 1<<20)
	var header struct {
		Account string `json:"account_id"`
	}
	if err != nil || json.Unmarshal(raw, &header) != nil || wechatapp.ConnectionID(header.Account) != id {
		websession.Fail(w, 404, "私聊投递记录不存在")
		return
	}
	row, err := readWechatCrossDelivery(root, key, header.Account)
	if err != nil {
		websession.Fail(w, 404, "私聊投递记录不可核对")
		return
	}
	worker.deliveryMu.Lock()
	err = worker.deliveryJournal().SaveStop(wechatapp.DeliveryStop{Account: row.Account, Thread: row.Origin, Trace: row.Trace, Key: key, At: time.Now().UTC()})
	worker.deliveryMu.Unlock()
	if err != nil {
		websession.Fail(w, 503, "停止意图尚未保存")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if native, err := s.WorkerSender(ctx, id); err == nil && row.matchesNative(ctx, native) {
		for _, part := range row.Parts {
			if !part.Attempted {
				continue
			}
			status, err := native.LookupSend(ctx, part.Key)
			if err == nil && row.matchesStatus(part, status) && status.State != "accepted" && status.State != "failed" && status.State != "cancelled" && status.State != "rejected" {
				_, _ = native.CancelMediaJob(ctx, status.ID)
			}
		}
	}
	websession.Respond(w, http.StatusAccepted, map[string]any{"idempotency_key": key, "state": "cancellation_requested", "cancel_requested": true, "scope": "originating_turn"})
}

var _ companion.PrivateReplyRouter = (*stationAttachmentTools)(nil)
