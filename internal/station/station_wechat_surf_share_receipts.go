package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
)

const stationSurfReceiptStart = "surf-share-start"

// A separate immutable stop per share stays writable while Submit holds the
// sender lock. The shared worker journal serializes an entire native request.
func surfShareCancelled(root *os.Root, row stationSurfShare) (bool, error) {
	raw, err := readLegacyImportFile(root, row.Key+".stop", 4<<10)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	var stop wechatapp.DeliveryStop
	if err != nil || json.Unmarshal(raw, &stop) != nil || stop.Account != row.Account || stop.Thread != row.Target || stop.Trace != row.Key || stop.Key != row.Key || stop.At.IsZero() {
		return false, apphost.ErrUnavailable
	}
	return true, nil
}

func (s *stationWechat) loadSurfShare(id, key string) (*stationWechatWorker, *os.Root, stationSurfShare, error) {
	if !artifactID(id, "wechat-") || !stationSurfReceiptKey(key) {
		return nil, nil, stationSurfShare{}, apphost.ErrNotFound
	}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		return nil, nil, stationSurfShare{}, apphost.ErrNotFound
	}
	root, err := openSurfShares(worker, false)
	if err != nil {
		return nil, nil, stationSurfShare{}, apphost.ErrNotFound
	}
	raw, err := readLegacyImportFile(root, key+".json", 64<<10)
	var header struct {
		Account string `json:"account"`
	}
	if err != nil || json.Unmarshal(raw, &header) != nil || wechatapp.ConnectionID(header.Account) != id {
		root.Close()
		return nil, nil, stationSurfShare{}, apphost.ErrNotFound
	}
	row, err := readSurfShare(root, key, header.Account)
	if err != nil {
		root.Close()
		return nil, nil, stationSurfShare{}, apphost.ErrUnavailable
	}
	return worker, root, row, nil
}

func (s *stationWechat) readSurfShareReceipt(ctx context.Context, id, key string) (apphost.Receipt, error) {
	worker, root, initial, err := s.loadSurfShare(id, key)
	if err != nil {
		return apphost.Receipt{}, err
	}
	defer root.Close()
	// Atomic checkpoints stay readable while a native submit is in flight.
	// Refresh only when both process and file serialization are available.
	refresh := worker.surfShareMu.TryLock()
	if refresh {
		defer worker.surfShareMu.Unlock()
		unlock, err := hostfile.TryLock(filepath.Join(worker.dir, "surf-delivery.lock"))
		if err != nil {
			refresh = false
		} else {
			defer unlock()
		}
	}
	row, err := readSurfShare(root, key, initial.Account)
	if err != nil {
		return apphost.Receipt{}, err
	}
	if !refresh {
		out := s.surfShareReceipt(id, row)
		out.CancelRequested, err = surfShareCancelled(root, row)
		return out, err
	}
	if native, err := s.WorkerSender(ctx, id); err == nil && native.Account() == row.Account && row.Attempted {
		dataset, err := native.Identity(ctx)
		if err == nil && dataset == row.Dataset {
			status, err := native.LookupSend(ctx, row.Key)
			if err == nil && row.matches(status) {
				row.Status = status
				if err := saveSurfShare(root, row); err != nil {
					return apphost.Receipt{}, err
				}
			}
		}
	}
	out := s.surfShareReceipt(id, row)
	out.CancelRequested, err = surfShareCancelled(root, row)
	return out, err
}

func (s *stationWechat) listSurfShares(ctx context.Context, id, after string, limit int) (apphost.ReceiptPage, error) {
	page := apphost.ReceiptPage{Items: []apphost.Receipt{}, Coverage: "surf_share_checkpoints"}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		return page, err
	}
	root, err := openSurfShares(worker, false)
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
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && stationSurfReceiptKey(key) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if after == stationSurfReceiptStart {
		after = ""
	}
	for _, key := range keys {
		if key <= after {
			continue
		}
		if len(page.Items) == limit {
			page.NextAfter = page.Items[len(page.Items)-1].IdempotencyKey
			break
		}
		_, selected, row, err := s.loadSurfShare(id, key)
		if err != nil {
			return page, err
		}
		out := s.surfShareReceipt(id, row)
		out.CancelRequested, err = surfShareCancelled(selected, row)
		selected.Close()
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, out)
	}
	page.Snapshot = localCheckpointKey(keys, page.Items)
	return page, ctx.Err()
}

func (s *stationWechat) cancelSurfShare(w http.ResponseWriter, r *http.Request, id, key string) {
	_, root, row, err := s.loadSurfShare(id, key)
	if err != nil {
		websession.Fail(w, 404, "分享记录不存在")
		return
	}
	defer root.Close()
	// Per-key stop lock never waits on native submission. Atomic rename means
	// dispatch and receipt readers see either no stop or the complete intent.
	unlock, err := hostfile.TryLock(filepath.Join(root.Name(), key+".stop.lock"))
	if err != nil {
		websession.Fail(w, 409, "停止意图正在保存，请稍后重试")
		return
	}
	stopped, err := surfShareCancelled(root, row)
	if err == nil && !stopped {
		temp := ".surf-stop-" + identity.RandomToken()
		defer root.Remove(temp)
		err = writeLegacyStagedFile(root, temp, wechatapp.DeliveryStop{Account: row.Account, Thread: row.Target, Trace: row.Key, Key: row.Key, At: time.Now().UTC()})
		if err == nil {
			err = root.Rename(temp, key+".stop")
		}
	}
	unlock()
	if err != nil {
		websession.Fail(w, 503, "停止意图尚未保存")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if native, err := s.WorkerSender(ctx, id); err == nil && native.Account() == row.Account && row.Attempted {
		dataset, err := native.Identity(ctx)
		if err == nil && dataset == row.Dataset {
			status, err := native.LookupSend(ctx, row.Key)
			if err == nil && row.matches(status) && status.State != "accepted" && status.State != "failed" && status.State != "cancelled" && status.State != "rejected" {
				_, _ = native.CancelMediaJob(ctx, status.ID)
			}
		}
	}
	websession.Respond(w, 202, map[string]any{"idempotency_key": key, "state": "cancellation_requested", "cancel_requested": true})
}
