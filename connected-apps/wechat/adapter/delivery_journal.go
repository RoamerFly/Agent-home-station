package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
	"wechat-bot/internal/workspacefs"
)

const controlJournalBytes = 2 << 20

// DeliveryJournal is the existing private per-worker runtime directory.
// Its caller serializes journal operations with entry to native submission.
type DeliveryJournal string

func readControlJournal[T any](dir DeliveryJournal, name string) (map[string]T, error) {
	rows := map[string]T{}
	root, err := workspacefs.OpenPath(string(dir))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return rows, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > controlJournalBytes {
		return nil, errors.New("control journal unavailable")
	}
	f, err := workspacefs.OpenRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, controlJournalBytes+1))
	if err != nil || len(raw) > controlJournalBytes || json.Unmarshal(raw, &rows) != nil || rows == nil || len(rows) > 5000 {
		return nil, errors.New("invalid control journal")
	}
	return rows, nil
}

func writeControlJournal[T any](dir DeliveryJournal, name string, rows map[string]T) error {
	raw, err := json.Marshal(rows)
	if err != nil || len(raw) > controlJournalBytes || len(rows) > 5000 {
		return errors.New("control journal full")
	}
	root, err := workspacefs.OpenPath(string(dir))
	if err != nil {
		return err
	}
	defer root.Close()
	stage := ".control-" + identity.RandomToken()
	defer root.Remove(stage)
	f, err := root.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return root.Rename(stage, name)
}

// Preserve the on-disk key derivation used before this journal moved into the adapter.
func deliveryControlKey(first, second, third string) string {
	raw, _ := json.Marshal([3]string{first, second, third})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (j DeliveryJournal) RequestResume(account, key, requestKey, thread, parentTrace string, rows map[string]ResumeControl, now time.Time) (ResumeControl, error) {
	id := "resume-" + deliveryControlKey(account, key, requestKey)
	control := ResumeControl{
		MediaResumeRequest: sending.MediaResumeRequest{ID: id, Account: account, Thread: thread, Key: key, Trace: "owner-" + id},
		MediaResumeResult:  sending.MediaResumeResult{State: "requested"},
		RequestKey:         requestKey, ParentTrace: parentTrace, Created: now,
	}
	rows[key] = control
	return control, writeControlJournal(j, "delivery-resumes.json", rows)
}

type DeliveryStop struct {
	Account string    `json:"account"`
	Thread  string    `json:"thread"`
	Trace   string    `json:"trace"`
	Key     string    `json:"key"`
	At      time.Time `json:"at"`
}

func DeliveryStopID(in companion.Input) string {
	return deliveryControlKey(in.Account, in.Thread, in.TraceID)
}

func (j DeliveryJournal) ReadStops() (map[string]DeliveryStop, error) {
	rows, err := readControlJournal[DeliveryStop](j, "delivery-stops.json")
	if err != nil {
		return nil, err
	}
	for key, row := range rows {
		if row.Account == "" || row.Thread == "" || row.Trace == "" || row.Key == "" || row.At.IsZero() || key != DeliveryStopID(companion.Input{Account: row.Account, Thread: row.Thread, TraceID: row.Trace}) {
			return nil, errors.New("invalid delivery cancellation identity")
		}
	}
	return rows, nil
}

func (j DeliveryJournal) SaveStop(stop DeliveryStop) error {
	rows, err := j.ReadStops()
	if err != nil {
		return err
	}
	id := DeliveryStopID(companion.Input{Account: stop.Account, Thread: stop.Thread, TraceID: stop.Trace})
	if _, exists := rows[id]; exists {
		return nil
	}
	if len(rows) >= 5000 {
		return errors.New("delivery cancellation journal full")
	}
	rows[id] = stop
	return writeControlJournal(j, "delivery-stops.json", rows)
}

func (j DeliveryJournal) WithSubmit(ctx context.Context, in companion.Input, submit func() error) error {
	rows, err := j.ReadStops()
	if err != nil {
		return err
	}
	if _, cancelled := rows[DeliveryStopID(in)]; cancelled {
		return bot.ErrDeliveryCancelled
	}
	controls, err := j.ReadResumes()
	if err != nil {
		return err
	}
	for _, r := range controls {
		if r.Account == in.Account && r.Thread == in.Thread && r.Trace == in.TraceID && r.State == "cancelled" {
			return bot.ErrDeliveryCancelled
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return submit()
}

type ResumeControl struct {
	sending.MediaResumeRequest
	sending.MediaResumeResult
	RequestKey  string    `json:"request_key"`
	ParentTrace string    `json:"parent_trace"`
	Created     time.Time `json:"created"`
	Next        time.Time `json:"next,omitempty"`
}

// CancelResumes runs under the caller’s delivery lock after the original stop is durable. Stop every
// queued continuation descended from that turn before releasing the send gate.
func (j DeliveryJournal) CancelResumes(stop DeliveryStop) (map[string]bool, error) {
	traces := map[string]bool{stop.Trace: true}
	rows, err := j.ReadResumes()
	if err != nil {
		return traces, err
	}
	changed := false
	for added := true; added; {
		added = false
		for key, r := range rows {
			if r.Account != stop.Account || r.Thread != stop.Thread || !traces[r.ParentTrace] && !traces[r.Trace] {
				continue
			}
			if !traces[r.Trace] {
				if err := j.SaveStop(DeliveryStop{Account: r.Account, Thread: r.Thread, Trace: r.Trace, Key: r.Key, At: stop.At}); err != nil {
					return traces, err
				}
				traces[r.Trace], added = true, true
			}
			if r.State != "cancelled" {
				r.State, r.Reason = "cancelled", "media_cancelled_before_native_submission"
				rows[key] = r
				changed = true
			}
		}
	}
	if changed {
		err = writeControlJournal(j, "delivery-resumes.json", rows)
	}
	return traces, err
}

func (j DeliveryJournal) ReadResumes() (map[string]ResumeControl, error) {
	rows, err := readControlJournal[ResumeControl](j, "delivery-resumes.json")
	if err != nil {
		return nil, err
	}
	for key, r := range rows {
		if key != r.Key || r.ID != "resume-"+deliveryControlKey(r.Account, r.Key, r.RequestKey) || r.Trace != "owner-"+r.ID || r.Thread == "" || r.Account == "" || !identity.ValidPathID(r.RequestKey) || r.Created.IsZero() || r.ParentTrace == "" {
			return nil, errors.New("invalid resume control identity")
		}
		switch r.State {
		case "requested", "waiting_receipt", "tracking", "blocked", "cancelled":
		default:
			return nil, errors.New("invalid resume control state")
		}
	}
	return rows, nil
}

func (j DeliveryJournal) NextResume(now time.Time) (*sending.MediaResumeRequest, error) {
	rows, err := j.ReadResumes()
	if err != nil {
		return nil, err
	}
	var next *ResumeControl
	for _, r := range rows {
		if (r.State == "requested" || r.State == "waiting_receipt") && !now.Before(r.Next) && (next == nil || r.Created.Before(next.Created) || r.Created.Equal(next.Created) && r.ID < next.ID) {
			copy := r
			next = &copy
		}
	}
	if next == nil {
		return nil, nil
	}
	request := next.MediaResumeRequest
	return &request, nil
}

func (j DeliveryJournal) RecordResumeResult(request sending.MediaResumeRequest, result sending.MediaResumeResult) error {
	rows, err := j.ReadResumes()
	if err != nil {
		return err
	}
	r, exists := rows[request.Key]
	if !exists || r.MediaResumeRequest != request {
		return errors.New("resume control changed")
	}
	if r.State == "cancelled" {
		return nil
	} // A late actor result cannot undo stop.
	r.MediaResumeResult = result
	r.Next = time.Time{}
	if result.State == "waiting_receipt" {
		r.Next = time.Now().Add(bot.DeliveryRetryBackoff(1))
	}
	rows[request.Key] = r
	return writeControlJournal(j, "delivery-resumes.json", rows)
}

func ResumeControlView(r ResumeControl) *apphost.ReceiptContinuation {
	message := map[string]string{"requested": "续办请求已保存", "waiting_receipt": "等待原任务分段回执", "tracking": "已建立续发任务", "blocked": "暂不能续办，请核对原任务", "cancelled": "续办已停止"}[r.State]
	if r.State == "blocked" {
		switch r.Reason {
		case "media_resume_missing_not_authorized":
			message = "续办权限已变化"
		case "media_resume_missing_source_invalid":
			message = "原素材或来源已变化"
		case "media_expected_voice_mismatch", "media_expected_voice_profile_mismatch":
			message = "指定声线与原成品不符"
		case "native_resume_missing_not_supported":
			message = "当前原生服务不支持分段续办"
		case "media_resume_original_not_submitted":
			message = "原语音未提交分段任务，可重新请求发送原成品"
		case "media_resume_already_accepted":
			message = "原语音已完整送达，无需续发"
		}
	}
	return &apphost.ReceiptContinuation{State: r.State, Message: message, Code: r.Reason, DeliveryKey: r.DeliveryKey}
}

func CancelWorkerMedia(ctx context.Context, native *linuxnative.Sender, key string, row bot.DeliveryEvidence) {
	if native == nil || row.Status.State == "prepared" || row.Status.State == "accepted" || row.Status.Kind == "text" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var status linuxnative.ToolStatus
	var err error
	if row.Status.ID != "" {
		status, err = native.ToolSendStatus(ctx, row.Status.ID)
	} else {
		status, err = native.LookupSend(ctx, key)
	}
	if err != nil || status.ID == "" || row.Status.ID != "" && status.ID != row.Status.ID || status.ThreadID != row.Status.ThreadID || status.State == "accepted" {
		return
	}
	if row.ResumeOriginID != "" && status.ID == row.ResumeOriginID {
		return
	}
	if row.StrictSource && (status.SourceID == "" || status.SourceID != row.SourceID || status.AccountID != native.Account() || status.Kind != "send" && status.Kind != row.Status.Kind) {
		return
	}
	_, _ = native.CancelMediaJob(ctx, status.ID)
}
