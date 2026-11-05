package bot

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"wechat-bot/internal/linuxnative"
)

// DeliveryEvidence is a read-only projection of the last atomic worker
// checkpoint. It excludes message content and cannot schedule or recover work.
type DeliveryEvidence struct {
	Trace, SourceID string
	Status          linuxnative.ToolStatus
}

const MaxDeliveryCheckpointBytes = 24 << 20

type deliveryCheckpointIdentity struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Trace string `json:"trace"`
	State string `json:"state"`
	Input struct {
		Account string `json:"account"`
		Thread  string `json:"thread"`
	} `json:"input"`
}

func ReadDeliveryEvidence(dir, account string, keys []string) (map[string]DeliveryEvidence, error) {
	out := make(map[string]DeliveryEvidence, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	f, err := os.Open(filepath.Join(dir, "messages-v2.json"))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxDeliveryCheckpointBytes+1))
	if err != nil {
		return nil, err
	}
	return decodeDeliveryEvidence(raw, account, keys)
}

// LegacyDeliveryEvidence projects a stopped queue without installing any
// replayable state. The account must match both the queue and its optional
// cursor checkpoint. Its server ID is retained as historical provenance.
func LegacyDeliveryEvidence(raw, cursor []byte, account string) (server string, records map[string]DeliveryEvidence, err error) {
	if len(raw) == 0 && len(cursor) == 0 {
		return "", nil, nil
	}
	var head struct {
		Version int
		Server  string
		Account string
	}
	if len(raw) > 0 && (len(raw) > MaxDeliveryCheckpointBytes || json.Unmarshal(raw, &head) != nil || head.Version != 1 || head.Server == "" || head.Account != account) {
		return "", nil, errors.New("legacy queue identity mismatch")
	}
	if len(cursor) > 0 {
		var c checkpoint
		if len(cursor) > 128<<10 || json.Unmarshal(cursor, &c) != nil || c.Version != 1 || c.ServerID == "" || c.Account != account || head.Server != "" && head.Server != c.ServerID {
			return "", nil, errors.New("legacy cursor identity mismatch")
		}
		head.Server = c.ServerID
	}
	if len(raw) == 0 {
		return head.Server, nil, nil
	}
	items, err := decodeDeliveryEvidence(raw, account, nil)
	return head.Server, items, err
}

func decodeDeliveryEvidence(raw []byte, account string, keys []string) (map[string]DeliveryEvidence, error) {
	out := make(map[string]DeliveryEvidence, len(keys))
	var snapshot struct {
		Version    int    `json:"version"`
		Account    string `json:"account"`
		Deliveries []*struct {
			deliveryCheckpointIdentity
			Thread        string   `json:"thread"`
			Reason        string   `json:"reason"`
			ServerID      string   `json:"server_id"`
			Cancelled     bool     `json:"cancelled"`
			SubmittedText string   `json:"submitted_text"`
			PreviousKeys  []string `json:"previous_keys"`
		} `json:"deliveries"`
		Media []*struct {
			deliveryCheckpointIdentity
			Kind          string                 `json:"kind"`
			Reason        string                 `json:"reason"`
			SourceID      string                 `json:"source_id"`
			UserCancelled bool                   `json:"user_cancelled"`
			Status        linuxnative.ToolStatus `json:"status"`
		} `json:"media"`
	}
	if len(raw) > MaxDeliveryCheckpointBytes || json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != 1 || snapshot.Account != account || len(snapshot.Deliveries) > 3000 || len(snapshot.Media) > 1000 {
		return nil, errors.New("invalid delivery checkpoint identity")
	}
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[key] = true
	}
	hasKey := func(key string) bool { return key != "" && (keys == nil || wanted[key]) }
	add := func(key string, row DeliveryEvidence) error {
		if _, duplicate := out[key]; duplicate || row.Status.ThreadID == "" {
			return errors.New("ambiguous delivery checkpoint identity")
		}
		out[key] = row
		return nil
	}
	for _, d := range snapshot.Deliveries {
		if d == nil {
			continue
		}
		for _, key := range d.PreviousKeys {
			if hasKey(key) {
				if d.Input.Account != account || d.Input.Thread != d.Thread {
					return nil, errors.New("previous text delivery checkpoint scope mismatch")
				}
				if err := add(key, DeliveryEvidence{Trace: d.Trace, Status: linuxnative.ToolStatus{Kind: "text", ThreadID: d.Thread, State: "unknown", CancelRequested: d.Cancelled}}); err != nil {
					return nil, err
				}
			}
		}
		if !hasKey(d.Key) {
			continue
		}
		if d.Input.Account != account || d.Input.Thread != d.Thread {
			return nil, errors.New("text delivery checkpoint scope mismatch")
		}
		status := linuxnative.ToolStatus{Kind: "text", ID: d.ID, ThreadID: d.Thread, State: d.State, ServerID: d.ServerID, Reason: d.Reason, CancelRequested: d.Cancelled}
		if d.State == "accepted" && len(d.PreviousKeys) != 0 {
			// A late receipt from an earlier attempt can settle this delivery.
			// The checkpoint does not identify which key produced that receipt.
			status.ID, status.State, status.ServerID = "", "unknown", ""
		}
		if d.State == "prepared" && d.SubmittedText != "" {
			status.State = "unknown"
		}
		if d.Cancelled && d.State == "stopped" && d.ID == "" && d.SubmittedText == "" && len(d.PreviousKeys) == 0 {
			status.State = "cancelled"
		}
		if err := add(d.Key, DeliveryEvidence{Trace: d.Trace, Status: status}); err != nil {
			return nil, err
		}
	}
	for _, d := range snapshot.Media {
		if d == nil || !hasKey(d.Key) {
			continue
		}
		status := d.Status
		if d.Input.Account != account || (status.ThreadID != "" && status.ThreadID != d.Input.Thread) ||
			(status.SourceID != "" && status.SourceID != d.SourceID) || (status.ID != "" && status.ID != d.ID) {
			return nil, errors.New("media delivery checkpoint scope mismatch")
		}
		status.Kind, status.ID, status.ThreadID = d.Kind, d.ID, d.Input.Thread
		status.CancelRequested = status.CancelRequested || d.UserCancelled
		if status.State == "" {
			switch {
			case d.State == "prepared":
				status.State = "prepared"
			case d.State == "stopped" && d.UserCancelled && d.Reason == "media_cancelled_before_native_submission":
				status.State = "cancelled"
			default:
				status.State = "unknown"
			}
		}
		if err := add(d.Key, DeliveryEvidence{Trace: d.Trace, SourceID: d.SourceID, Status: status}); err != nil {
			return nil, err
		}
	}
	return out, nil
}
