package station

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

type legacyReceiptPage struct {
	apphost.ReceiptPage
	ReceiptCount int `json:"receipt_count"`
	PendingCount int `json:"pending_count"`
}

func (s *stationWechat) legacyReceiptRoutes(server *websession.Store, mux *http.ServeMux, path string) {
	mux.HandleFunc("GET "+path, server.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		query := r.URL.Query()
		limit := 30
		if raw := query.Get("limit"); raw != "" {
			var err error
			if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > 200 {
				websession.Fail(w, http.StatusBadRequest, "每页请输入 1 到 200 条")
				return
			}
		}
		pending := query.Get("pending")
		if pending != "" && pending != "1" {
			websession.Fail(w, http.StatusBadRequest, "待核对筛选参数无效")
			return
		}
		cursor := query.Get("after")
		revision, key, _ := strings.Cut(cursor, ":")
		if cursor != "" && (!legacyMemoryDigestValid(revision) || !identity.ValidPathID(key)) {
			websession.Fail(w, http.StatusBadRequest, "旧回执游标无效，请重新查看")
			return
		}
		page, err := s.listLegacyReceipts(r.PathValue("connection"), revision, key, limit, pending == "1")
		if err != nil {
			legacyImportFailure(w, err)
			return
		}
		websession.Respond(w, http.StatusOK, page)
	}))
	mux.HandleFunc("GET "+path+"/{key}", server.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id, key := r.PathValue("connection"), r.PathValue("key")
		archive, receipt, err := s.legacyReceiptAt(id, key)
		if errors.Is(err, apphost.ErrNotFound) {
			websession.Fail(w, http.StatusNotFound, "此连接没有这条旧投递回执")
			return
		}
		if err != nil {
			legacyImportFailure(w, err)
			return
		}
		if legacyReceiptPending(receipt) {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			// The saved record stays available during a native outage. The
			// check itself uses a freshly roster-bound native transport.
			if receipt.OperationID == "" {
				receipt.Lookup = "legacy_operation_unknown"
			} else if archive.LegacyServer == "" {
				receipt.Lookup = "legacy_dataset_mismatch"
			} else if native, err := s.WorkerSender(ctx, id); err != nil {
				receipt.Lookup = "legacy_native_unavailable"
			} else {
				receipt = checkLegacyReceipt(ctx, native, archive, receipt)
			}
		}
		websession.Respond(w, http.StatusOK, receipt)
	}))
}

func (s *stationWechat) legacyReceiptArchive(id string) (legacyImportArchive, error) {
	if !artifactID(id, "wechat-") {
		return legacyImportArchive{}, apphost.ErrNotFound
	}
	root, err := workspacefs.OpenPath(filepath.Join(s.root, id))
	if errors.Is(err, os.ErrNotExist) {
		return legacyImportArchive{}, apphost.ErrNotFound
	}
	if err != nil {
		return legacyImportArchive{}, apphost.ErrUnavailable
	}
	defer root.Close()
	archive, err := readLegacyImportArchive(root)
	if errors.Is(err, os.ErrNotExist) {
		return legacyImportArchive{}, apphost.ErrNotFound
	}
	if err != nil || wechatapp.ConnectionID(archive.Plan.Account) != id || !legacyMemoryDigestValid(archive.Plan.Digest) {
		return legacyImportArchive{}, errLegacyImportInvalid
	}
	return archive, nil
}

func validLegacyReceipt(receipt apphost.Receipt, ref apphost.ConnectionRef, key string) bool {
	return receipt.ConnectionRef == ref && receipt.IdempotencyKey == key && identity.ValidPathID(key) &&
		identity.ValidPathID(receipt.ConversationID) && (receipt.OperationID == "" || identity.ValidPathID(receipt.OperationID)) &&
		receipt.Lookup == "legacy_snapshot"
}

func legacyReceiptPending(receipt apphost.Receipt) bool {
	return receipt.State == "submission_unknown" || receipt.State == "submitted_pending" || receipt.PartsIncomplete
}

func (s *stationWechat) listLegacyReceipts(id, revision, after string, limit int, pendingOnly bool) (legacyReceiptPage, error) {
	archive, err := s.legacyReceiptArchive(id)
	if err != nil {
		return legacyReceiptPage{}, err
	}
	page := legacyReceiptPage{ReceiptPage: apphost.ReceiptPage{Items: []apphost.Receipt{}, Coverage: "legacy_snapshot"}, ReceiptCount: len(archive.Receipts)}
	page.Snapshot = localCheckpointKey("legacy-receipts-v1", id, archive.Plan.Digest, archive.LegacyServer, archive.Receipts, archive.ReceiptSources)
	marker := localCheckpointKey(page.Snapshot, pendingOnly)
	if revision != "" {
		item, exists := archive.Receipts[after]
		if revision != marker || !exists || pendingOnly && !legacyReceiptPending(item) {
			return legacyReceiptPage{}, errLegacyImportChanged
		}
	}
	ref := s.connectionRef(id, archive.Plan.Account)
	keys := make([]string, 0, len(archive.Receipts))
	for key, receipt := range archive.Receipts {
		if !validLegacyReceipt(receipt, ref, key) {
			return legacyReceiptPage{}, errLegacyImportInvalid
		}
		pending := legacyReceiptPending(receipt)
		if pending {
			page.PendingCount++
		}
		if key > after && (!pendingOnly || pending) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if len(keys) > limit {
		keys = keys[:limit]
		page.NextAfter = marker + ":" + keys[len(keys)-1]
	}
	for _, key := range keys {
		page.Items = append(page.Items, archive.Receipts[key])
	}
	return page, nil
}

func (s *stationWechat) legacyReceiptAt(id, key string) (legacyImportArchive, apphost.Receipt, error) {
	if !identity.ValidPathID(key) {
		return legacyImportArchive{}, apphost.Receipt{}, apphost.ErrNotFound
	}
	archive, err := s.legacyReceiptArchive(id)
	if err != nil {
		return legacyImportArchive{}, apphost.Receipt{}, err
	}
	receipt, exists := archive.Receipts[key]
	if !exists {
		return legacyImportArchive{}, apphost.Receipt{}, apphost.ErrNotFound
	}
	if !validLegacyReceipt(receipt, s.connectionRef(id, archive.Plan.Account), key) {
		return legacyImportArchive{}, apphost.Receipt{}, errLegacyImportInvalid
	}
	return archive, receipt, nil
}

func (s *stationWechat) readLegacyReceipt(ctx context.Context, id, key string, native *linuxnative.Sender) (apphost.Receipt, error) {
	archive, receipt, err := s.legacyReceiptAt(id, key)
	if errors.Is(err, errLegacyImportInvalid) {
		return apphost.Receipt{}, apphost.ErrUnavailable
	}
	if err != nil || !legacyReceiptPending(receipt) || receipt.OperationID == "" || archive.LegacyServer == "" {
		return receipt, err
	}
	receipt = checkLegacyReceipt(ctx, native, archive, receipt)
	// Keep the original fallback API's local snapshot result when the
	// native instance or dataset cannot be verified.
	if receipt.Lookup == "legacy_native_unavailable" || receipt.Lookup == "legacy_dataset_mismatch" {
		receipt.Lookup = "legacy_snapshot"
	}
	return receipt, nil
}

func checkLegacyReceipt(ctx context.Context, native *linuxnative.Sender, archive legacyImportArchive, receipt apphost.Receipt) apphost.Receipt {
	if native.Account() != receipt.AccountID {
		receipt.Lookup = "identity_mismatch"
		return receipt
	}
	dataset, err := native.DaemonStatus(ctx)
	if err != nil {
		receipt.Lookup = "legacy_native_unavailable"
		return receipt
	}
	if dataset.ServerID != archive.LegacyServer {
		receipt.Lookup = "legacy_dataset_mismatch"
		return receipt
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := native.ToolSendStatus(ctx, receipt.OperationID)
	source := archive.ReceiptSources[receipt.IdempotencyKey]
	switch {
	case errors.Is(err, linuxnative.ErrToolNotFound):
		receipt.Lookup = "not_found"
	case err != nil:
		receipt.Lookup = "unavailable"
	case status.ID != receipt.OperationID || status.ThreadID != receipt.ConversationID ||
		status.Kind != "" && status.Kind != "send" && status.Kind != receipt.Kind || source != "" && status.SourceID != source:
		receipt.Lookup = "identity_mismatch"
	default:
		latest, err := native.DaemonStatus(ctx)
		if err != nil || latest != dataset {
			receipt.Lookup = "legacy_native_unavailable"
		} else if projectLegacyReceipt(&receipt.DeliveryReceipt, status) {
			receipt.Lookup = "platform"
		} else {
			receipt.Lookup = "identity_mismatch"
		}
	}
	return receipt
}

// A later observation cannot erase confirmed parts or report a conflicting
// part ID as success. Preserve old unconfirmed parts absent from the reply.
func projectLegacyReceipt(receipt *apphost.DeliveryReceipt, status linuxnative.ToolStatus) bool {
	chunks := make(map[int]linuxnative.VoiceChunkStatus, len(status.VoiceChunks))
	for _, chunk := range status.VoiceChunks {
		if _, duplicate := chunks[chunk.Index]; duplicate {
			return false
		}
		chunks[chunk.Index] = chunk
	}
	for _, part := range receipt.AcceptedParts {
		chunk, found := chunks[part.Index]
		if found && chunk.HasReceipt && chunk.ServerID != part.ServerID {
			return false
		}
		if !found || !chunk.HasReceipt {
			status.VoicePartsIncomplete = true
		}
	}
	var omitted []apphost.ReceiptPart
	for _, part := range receipt.UnconfirmedParts {
		if _, found := chunks[part.Index]; !found {
			status.VoicePartsIncomplete = true
			omitted = append(omitted, part)
		}
	}
	if receipt.PartsIncomplete && len(chunks) == 0 {
		status.VoicePartsIncomplete = true
	}
	wechatapp.ProjectDeliveryReceipt(receipt, status)
	receipt.UnconfirmedParts = append(receipt.UnconfirmedParts, omitted...)
	return true
}
