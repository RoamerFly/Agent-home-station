package bot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"

	"wechat-bot/internal/companion"
)

type TaskDetail struct {
	Task    TaskEvidence
	Sources []*companion.SourceRef
	Dataset string
}

// ReadTaskDetailFrom keeps private checkpoint inputs inside the host. Source
// references are candidates for the host's current authorization checks.
func ReadTaskDetailFrom(reader io.Reader, account, id string) (TaskDetail, error) {
	var detail TaskDetail
	raw, err := io.ReadAll(io.LimitReader(reader, MaxDeliveryCheckpointBytes+1))
	if err != nil || len(raw) > MaxDeliveryCheckpointBytes {
		return detail, errors.New("task checkpoint unavailable")
	}
	page, err := ReadTaskEvidencePageFrom(bytes.NewReader(raw), account, "", "", math.MaxInt)
	if err != nil {
		return detail, err
	}
	found := false
	for _, row := range page.Items {
		if row.ID == id {
			detail.Task, found = row, true
			break
		}
	}
	if !found {
		return detail, errors.New("task not found")
	}
	var checkpoint runtimeState
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return TaskDetail{}, err
	}
	detail.Dataset = checkpoint.Server
	var input *companion.Input
	for _, plan := range checkpoint.Plans {
		if "plan-"+plan.ID == id {
			input = &plan.Input
		}
	}
	for _, media := range checkpoint.Media {
		if "media-"+media.Key == id {
			input = &media.Input
		}
	}
	for _, reply := range checkpoint.MediaReplies {
		if "media-reply-"+reply.Trace == id {
			input = &reply.Input
		}
	}
	for _, delivery := range checkpoint.Deliveries {
		if "delivery-"+delivery.Key == id {
			input = &delivery.Input
		}
	}
	for _, wake := range checkpoint.Wakes {
		if "wake-"+wake.Input.Thread+"-"+wake.Parent == id {
			input = &wake.Input
		}
	}
	if input == nil {
		return detail, nil
	}
	seen := map[string]bool{}
	for _, source := range []*companion.SourceRef{input.Source, input.QuotedSource} {
		if source != nil && source.Account == account && source.Thread == detail.Task.Thread &&
			source.MessageID != "" && source.MessageType == 49 && !seen[source.MessageID] {
			seen[source.MessageID] = true
			detail.Sources = append(detail.Sources, source)
		}
	}
	return detail, nil
}
