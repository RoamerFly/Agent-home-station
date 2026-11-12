package bot

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"
)

// TaskEvidence exposes checkpoint metadata without message bodies, model
// inputs, tool results, generated text or local file paths.
type TaskEvidence struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Thread  string    `json:"thread"`
	Message string    `json:"message"`
	State   string    `json:"state"`
	Receipt string    `json:"receipt,omitempty"`
	At      time.Time `json:"at"`
}

type TaskEvidencePage struct {
	Items     []TaskEvidence `json:"items"`
	NextAfter string         `json:"next_after"`
	Limit     int            `json:"effective_limit"`
}

func ReadTaskEvidencePageFrom(reader io.Reader, account, thread, after string, limit int) (TaskEvidencePage, error) {
	page := TaskEvidencePage{Items: []TaskEvidence{}, Limit: limit}
	if account == "" || limit < 1 {
		return page, errors.New("invalid task page")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxDeliveryCheckpointBytes+1))
	var checkpoint runtimeState
	if err != nil || len(raw) > MaxDeliveryCheckpointBytes || json.Unmarshal(raw, &checkpoint) != nil ||
		!SupportedDeliveryCheckpointVersion(checkpoint.Version) || checkpoint.Account != account || checkpoint.Server == "" {
		return page, errors.New("task checkpoint unavailable")
	}
	rows := make(map[string]TaskEvidence)
	add := func(row TaskEvidence, sourceAccount string) error {
		if sourceAccount != account || row.ID == "" || row.Thread == "" || row.Message == "" {
			return errors.New("task identity mismatch")
		}
		if _, exists := rows[row.ID]; exists {
			return errors.New("duplicate task identity")
		}
		rows[row.ID] = row
		return nil
	}
	for _, item := range checkpoint.Inbox {
		id := item.Event.Data.MessageID
		if err := add(TaskEvidence{ID: "message-" + id, Kind: "message", Thread: item.Event.Data.ThreadID,
			Message: id, State: item.Phase, At: item.Updated}, account); err != nil {
			return page, err
		}
	}
	for _, plan := range checkpoint.Plans {
		if plan == nil || plan.ID == "" {
			return page, errors.New("invalid task plan")
		}
		if err := add(TaskEvidence{ID: "plan-" + plan.ID, Kind: "reply", Thread: plan.Input.Thread,
			Message: plan.Trace, State: plan.State, At: plan.Created}, plan.Input.Account); err != nil {
			return page, err
		}
	}
	for _, media := range checkpoint.Media {
		if media == nil || media.Key == "" {
			return page, errors.New("invalid media task")
		}
		if err := add(TaskEvidence{ID: "media-" + media.Key, Kind: media.Kind, Thread: media.Input.Thread,
			Message: media.Trace, State: media.State, Receipt: media.Key, At: media.Created}, media.Input.Account); err != nil {
			return page, err
		}
	}
	for _, reply := range checkpoint.MediaReplies {
		if reply == nil {
			return page, errors.New("invalid media reply task")
		}
		if err := add(TaskEvidence{ID: "media-reply-" + reply.Trace, Kind: "media_reply", Thread: reply.Input.Thread,
			Message: reply.Trace, State: reply.State, At: reply.Created}, reply.Input.Account); err != nil {
			return page, err
		}
	}
	for _, delivery := range checkpoint.Deliveries {
		if delivery == nil || delivery.Key == "" {
			return page, errors.New("invalid delivery task")
		}
		if err := add(TaskEvidence{ID: "delivery-" + delivery.Key, Kind: "delivery", Thread: delivery.Input.Thread,
			Message: delivery.Trace, State: delivery.State, Receipt: delivery.Key, At: delivery.Created}, delivery.Input.Account); err != nil {
			return page, err
		}
	}
	for _, wake := range checkpoint.Wakes {
		if wake == nil || wake.Parent == "" {
			return page, errors.New("invalid continuation task")
		}
		if err := add(TaskEvidence{ID: "wake-" + wake.Input.Thread + "-" + wake.Parent, Kind: "continuation",
			Thread: wake.Input.Thread, Message: wake.Parent, State: wake.State, At: wake.Due}, wake.Input.Account); err != nil {
			return page, err
		}
	}
	keys := make([]string, 0, len(rows))
	for id, row := range rows {
		if id > after && (thread == "" || row.Thread == thread) {
			keys = append(keys, id)
		}
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
		page.NextAfter = keys[len(keys)-1]
	}
	for _, id := range keys {
		page.Items = append(page.Items, rows[id])
	}
	return page, nil
}
