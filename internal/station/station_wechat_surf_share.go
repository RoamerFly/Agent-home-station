package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

type stationSurfShare struct {
	Account                                          string `json:"account"`
	Key                                              string `json:"key"`
	Target                                           string `json:"target"`
	Dataset                                          string `json:"dataset"`
	Grant                                            string `json:"grant"`
	Job, Source, SourceEpoch, Candidate, ContentHash string
	Title, URL, Description                          string
	Created                                          time.Time              `json:"created"`
	Attempted                                        bool                   `json:"attempted"`
	BodyUnverified                                   bool                   `json:"body_unverified,omitempty"`
	Status                                           linuxnative.ToolStatus `json:"status"`
}

func stationSurfReceiptKey(key string) bool {
	return strings.HasPrefix(key, "surf-share-") && identity.ValidPathID(key)
}

func openSurfShares(worker *stationWechatWorker, create bool) (*os.Root, error) {
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if create {
		return workspacefs.EnsureDirectory(root, "surf-deliveries")
	}
	return workspacefs.OpenExistingDirectory(root, "surf-deliveries")
}

func readSurfShare(root *os.Root, key, account string) (stationSurfShare, error) {
	var row stationSurfShare
	if !stationSurfReceiptKey(key) {
		return row, apphost.ErrNotFound
	}
	raw, err := readLegacyImportFile(root, key+".json", 64<<10)
	if err != nil {
		return row, err
	}
	if json.Unmarshal(raw, &row) != nil || row.Account != account || row.Key != key || !identity.ValidPathID(row.Target) ||
		row.Dataset == "" || !legacyMemoryDigestValid(row.Job) || !legacyMemoryDigestValid(row.Grant) || !legacyMemoryDigestValid(row.ContentHash) ||
		row.URL == "" || row.Title == "" || row.Created.IsZero() ||
		key != surfShareKey(row.Account, row.Target, row.URL) {
		return stationSurfShare{}, apphost.ErrUnavailable
	}
	if row.Status.ID != "" {
		row.Status.AccountID = account
	}
	return row, nil
}

func saveSurfShare(root *os.Root, row stationSurfShare) error {
	encoded, err := json.Marshal(row)
	if err != nil || len(encoded) > 64<<10 {
		return apphost.ErrObservationInvalid
	}
	temp := ".surf-share-" + identity.RandomToken()
	defer root.Remove(temp)
	if err := writeLegacyStagedFile(root, temp, row); err != nil {
		return err
	}
	return root.Rename(temp, row.Key+".json")
}

func surfShareKey(account, target, url string) string {
	return "surf-share-" + localCheckpointKey("source-url-v1", account, target, url)[:40]
}

func (row stationSurfShare) matches(status linuxnative.ToolStatus) bool {
	return status.AccountID == row.Account && status.ThreadID == row.Target && status.ID != "" &&
		(row.Status.ID == "" || row.Status.ID == status.ID) &&
		status.SourceID == "" && (status.Kind == "" || status.Kind == "share" || status.Kind == "send")
}

func (s *stationWechat) surfShareEvidence(ctx context.Context, worker *stationWechatWorker, host *stationShareHost, account, jobID string) (stationSurfShare, error) {
	if !legacyMemoryDigestValid(jobID) {
		return stationSurfShare{}, apphost.ErrNotFound
	}
	job, err := host.store.Job(ctx, jobID)
	if err != nil || !surfHistoryJob(account, job) || job.State != "completed" || job.CancelRequested {
		return stationSurfShare{}, apphost.ErrObservationForbidden
	}
	raw, err := host.store.PrivateJobResult(ctx, job, func(ctx context.Context, job pluginhost.Job) error {
		return surfHistorySourceAllowed(ctx, worker, host, account, job)
	})
	if err != nil {
		return stationSurfShare{}, err
	}
	var saved struct {
		URL       string `json:"url"`
		Hash      string `json:"sha256"`
		Candidate string `json:"candidate_id"`
	}
	var summary stationSurfReadSummary
	if json.Unmarshal(raw, &saved) != nil || json.Unmarshal(job.Result, &summary) != nil || !summary.Verified ||
		saved.URL == "" || !legacyMemoryDigestValid(saved.Hash) || saved.Candidate != summary.Candidate || summary.Returned < 1 {
		return stationSurfShare{}, apphost.ErrObservationForbidden
	}
	return stationSurfShare{Account: account, Job: job.ID, Source: summary.SourceID, SourceEpoch: job.Context.EvidenceEpoch,
		Candidate: saved.Candidate, ContentHash: saved.Hash, Title: summary.Title, URL: saved.URL}, nil
}

func (s *stationWechat) submitSurfShare(ctx context.Context, id, jobID, target, description string, automatic bool, allowed func() error) (apphost.Receipt, error) {
	if !artifactID(id, "wechat-") || !identity.ValidPathID(target) || len(description) > 3000 || !utf8.ValidString(description) || allowed == nil {
		return apphost.Receipt{}, apphost.ErrObservationInvalid
	}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	native, err := s.WorkerSender(ctx, id)
	if err != nil {
		return apphost.Receipt{}, err
	}
	host, err := s.shareHost(worker, native.Account())
	if err != nil {
		return apphost.Receipt{}, err
	}
	evidence, err := s.surfShareEvidence(ctx, worker, host, native.Account(), jobID)
	if err != nil {
		return apphost.Receipt{}, err
	}
	grant, _, _, err := s.surfShareAuthority(ctx, worker, native.Account())
	if err != nil {
		return apphost.Receipt{}, err
	}
	check := func() error {
		if err := allowed(); err != nil {
			return err
		}
		if err := s.checkSurfShareGrant(ctx, worker, native, grant, target, evidence.Source, evidence.SourceEpoch, automatic); err != nil {
			return err
		}
		current, err := s.surfShareEvidence(ctx, worker, host, native.Account(), jobID)
		if err != nil || localCheckpointKey(current) != localCheckpointKey(evidence) {
			return apphost.ErrObservationForbidden
		}
		return nil
	}
	if err := check(); err != nil {
		return apphost.Receipt{}, err
	}
	worker.surfShareMu.Lock()
	defer worker.surfShareMu.Unlock()
	root, err := openSurfShares(worker, true)
	if err != nil {
		return apphost.Receipt{}, err
	}
	defer root.Close()
	unlock, err := hostfile.TryLock(filepath.Join(worker.dir, "surf-delivery.lock"))
	if err != nil {
		return apphost.Receipt{}, apphost.ErrLifecycleConflict
	}
	defer unlock()
	key := surfShareKey(evidence.Account, target, evidence.URL)
	row, err := readSurfShare(root, key, evidence.Account)
	if errors.Is(err, os.ErrNotExist) {
		row = evidence
		row.Key, row.Target, row.Dataset, row.Grant = key, target, grant.Dataset, localCheckpointKey(grant)
		row.Description, row.Created = description, time.Now().UTC()
		if err := saveSurfShare(root, row); err != nil {
			return apphost.Receipt{}, err
		}
	} else if err != nil {
		return apphost.Receipt{}, err
	}
	cancelled, err := surfShareCancelled(root, row)
	if err != nil {
		return apphost.Receipt{}, err
	}
	if cancelled {
		out := s.surfShareReceipt(id, row)
		out.CancelRequested = true
		return out, apphost.ErrLifecycleConflict
	}
	if row.Attempted && row.Dataset != grant.Dataset {
		// The same account name on a replacement native dataset cannot resolve
		// or restart an old send, even after the owner grants new permissions.
		return s.surfShareReceipt(id, row), nil
	}
	status, lookupErr := native.LookupSend(ctx, row.Key)
	if row.Attempted {
		// A missing/unknown receipt is never permission to submit again.
		if lookupErr == nil && row.matches(status) {
			row.Status = status
			if err := saveSurfShare(root, row); err != nil {
				return apphost.Receipt{}, err
			}
		}
		return s.surfShareReceipt(id, row), nil
	}
	if lookupErr == nil {
		row.Attempted, row.BodyUnverified = true, true
		if row.matches(status) {
			row.Status = status
		}
		if err := saveSurfShare(root, row); err != nil {
			return apphost.Receipt{}, err
		}
		return s.surfShareReceipt(id, row), nil
	}
	if !errors.Is(lookupErr, linuxnative.ErrToolNotFound) {
		return s.surfShareReceipt(id, row), lookupErr
	}
	if err := check(); err != nil {
		return s.surfShareReceipt(id, row), err
	}
	// Nothing has been submitted under this key. A new, explicitly authorized
	// attempt can replace a prepared checkpoint after a source/grant change.
	// Attempted checkpoints above remain immutable and lookup-only forever.
	if row.Grant != localCheckpointKey(grant) || row.Job != evidence.Job || row.Dataset != grant.Dataset {
		created := row.Created
		row = evidence
		row.Key, row.Target, row.Dataset, row.Grant = key, target, grant.Dataset, localCheckpointKey(grant)
		row.Description, row.Created = description, created
	}
	row.Attempted = true
	if err := saveSurfShare(root, row); err != nil {
		return apphost.Receipt{}, err
	}
	err = func() error {
		if err := check(); err != nil {
			return err
		}
		if stopped, err := surfShareCancelled(root, row); err != nil || stopped {
			return apphost.ErrLifecycleConflict
		}
		var submitErr error
		status, submitErr = native.Submit(ctx, linuxnative.ToolSend{ThreadID: row.Target, Kind: "share",
			Title: row.Title, URL: row.URL, Description: row.Description, IdempotencyKey: row.Key})
		return submitErr
	}()
	if err == nil && row.matches(status) {
		row.Status = status
		if err := saveSurfShare(root, row); err != nil {
			return apphost.Receipt{}, err
		}
	}
	return s.surfShareReceipt(id, row), err
}

func (s *stationWechat) surfShareReceipt(id string, row stationSurfShare) apphost.Receipt {
	out := apphost.Receipt{ConnectionRef: s.connectionRef(id, row.Account), ConversationID: row.Target,
		DeliveryReceipt: apphost.DeliveryReceipt{IdempotencyKey: row.Key, OperationID: row.Status.ID, Kind: "share", Lookup: "surf_source_checkpoint", PlatformState: row.Status.State}}
	out.State = row.Status.State
	if !row.Attempted {
		out.State = "prepared"
	}
	if row.Attempted && (out.State == "" || out.State == "accepted" && row.Status.ServerID == "" || row.BodyUnverified) {
		out.State = "unknown"
	}
	if out.State == "accepted" {
		out.ServerID = row.Status.ServerID
		out.AcceptedParts = []apphost.ReceiptPart{{Index: 0, State: "accepted", ServerID: row.Status.ServerID}}
	} else {
		out.UnconfirmedParts = []apphost.ReceiptPart{{Index: 0, State: out.State}}
	}
	return out
}

func (s *stationWechat) surfShareRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Job         string `json:"job"`
		Target      string `json:"target"`
		Description string `json:"description"`
	}
	if !websession.Decode(w, r, &in) {
		return
	}
	owner, err := s.ownerReader()
	if err != nil {
		websession.Fail(w, 401, "请先登录本机主人")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	receipt, err := s.submitSurfShare(ctx, r.PathValue("connection"), in.Job, in.Target, in.Description, false, func() error {
		current, err := s.ownerReader()
		if err != nil || current != owner {
			return apphost.ErrObservationForbidden
		}
		return ctx.Err()
	})
	if receipt.IdempotencyKey != "" {
		websession.Respond(w, 200, receipt)
		return
	}
	if err != nil {
		websession.Fail(w, 409, "分享来源、目标或授权暂不可核对，请刷新配置和浏览记录")
		return
	}
	websession.Fail(w, 503, "分享暂未完成")
}
