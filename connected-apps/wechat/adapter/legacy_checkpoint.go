package adapter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
)

// LegacyCheckpointFile holds the exact bytes used for a preview or snapshot.
// The host owns the guarded read and any digest or archival storage.
type LegacyCheckpointFile struct {
	Path string
	Data []byte
}

type LegacyWorkerCheckpoint struct {
	Queue, Cursor LegacyCheckpointFile
	ServerID      string
	Deliveries    map[string]LegacyCheckpointDelivery
}

type LegacyCheckpointDelivery struct {
	SourceID, ResumedFrom string
	Status                linuxnative.ToolStatus
}

type LegacyCheckpointReadError struct {
	Cursor bool
	Err    error
}

func (e *LegacyCheckpointReadError) Error() string { return e.Err.Error() }
func (e *LegacyCheckpointReadError) Unwrap() error { return e.Err }

var errLegacyCheckpointIdentity = errors.New("legacy WeChat checkpoint identity mismatch")

// ReadLegacyWorkerCheckpoint reads the supported legacy formats through the
// host's pinned snapshot or audit reader. It cannot admit or replay a job.
func ReadLegacyWorkerCheckpoint(queueDir, account string, read func(string, int64) ([]byte, error)) (LegacyWorkerCheckpoint, error) {
	var result LegacyWorkerCheckpoint
	result.Queue.Path = filepath.Join(queueDir, "messages-v2.json")
	result.Cursor.Path = filepath.Join(queueDir, "checkpoint.json")
	var err error
	result.Queue.Data, err = read(result.Queue.Path, bot.MaxDeliveryCheckpointBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return LegacyWorkerCheckpoint{}, &LegacyCheckpointReadError{Err: err}
	}
	result.Cursor.Data, err = read(result.Cursor.Path, 128<<10)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return LegacyWorkerCheckpoint{}, &LegacyCheckpointReadError{Cursor: true, Err: err}
	}

	server, deliveries, err := bot.LegacyDeliveryEvidence(result.Queue.Data, result.Cursor.Data, account)
	if err != nil || server != "" && !identity.ValidPathID(server) {
		return LegacyWorkerCheckpoint{}, errLegacyCheckpointIdentity
	}
	result.ServerID = server
	result.Deliveries = make(map[string]LegacyCheckpointDelivery, len(deliveries))
	for key, delivery := range deliveries {
		status := delivery.Status
		if !identity.ValidPathID(key) || !identity.ValidPathID(status.ThreadID) || status.ID != "" && !identity.ValidPathID(status.ID) {
			return LegacyWorkerCheckpoint{}, errLegacyCheckpointIdentity
		}
		result.Deliveries[key] = LegacyCheckpointDelivery{SourceID: delivery.SourceID, ResumedFrom: delivery.ResumedFrom, Status: status}
	}
	return result, nil
}

// LegacyQueuePending is a read-only summary of the old worker's open entries.
type LegacyQueuePending struct {
	Deliveries, Inbox, Wakes, Plans, MediaReplies int
}

func (checkpoint LegacyWorkerCheckpoint) PendingWork() (LegacyQueuePending, error) {
	var result LegacyQueuePending
	type row struct{ State string }
	var worker struct {
		Inbox      []struct{ Phase string }
		Deliveries []*row
		Media      []*row
		Wakes      []row `json:"conversation_wakes"`
		Plans      []row `json:"utterance_plans"`
		Replies    []row `json:"media_replies"`
	}
	if checkpoint.Queue.Data != nil && json.Unmarshal(checkpoint.Queue.Data, &worker) != nil {
		return result, errors.New("invalid legacy WeChat pending work")
	}
	for _, item := range worker.Deliveries {
		if item != nil && item.State != "accepted" && item.State != "stopped" {
			result.Deliveries++
		}
	}
	for _, item := range worker.Media {
		if item != nil && item.State != "accepted" && item.State != "stopped" && item.State != "needs_review" {
			result.Deliveries++
		}
	}
	for _, item := range worker.Inbox {
		switch item.Phase {
		case "completed", "failed", "interrupted", "needs_review", "replied", "silent", "skipped", "stopped":
		default:
			result.Inbox++
		}
	}
	for _, item := range worker.Wakes {
		if item.State == "waiting" || item.State == "claimed" {
			result.Wakes++
		}
	}
	for _, item := range worker.Plans {
		if item.State != "accepted" && item.State != "stopped" {
			result.Plans++
		}
	}
	for _, item := range worker.Replies {
		if item.State != "accepted" && item.State != "stopped" && item.State != "queued_plan" {
			result.MediaReplies++
		}
	}
	return result, nil
}
