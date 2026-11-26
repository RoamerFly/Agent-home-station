package admin

import (
	"encoding/json"
	"time"

	"wechat-bot/internal/mcptools"
)

// The most recent job observation is authoritative for a task. A successful
// render poll can stay in_progress for many minutes without yielding new bytes.
func (t agentTask) waitingForComfyJob() bool {
	return t.runningComfyJobID() != ""
}

type comfyPollReceipt struct {
	JobID string `json:"job_id"`
	State string `json:"state"`
}

func (t agentTask) runningComfyJobID() string {
	for i := len(t.Actions) - 1; i >= 0; i-- {
		action := t.Actions[i]
		if action.Tool != "station_comfy" {
			continue
		}
		var input struct {
			Action    string `json:"action"`
			JobID     string `json:"job_id"`
			JobAction string `json:"job_action"`
		}
		if action.ComfyRun {
			input.Action = "run"
		} else if json.Unmarshal([]byte(action.Arguments), &input) != nil {
			return ""
		}
		if input.Action != "job" && input.Action != "run" {
			continue
		}
		if input.JobAction == "cancel" || !t.CorrectionAt.IsZero() && (!action.At.After(t.CorrectionAt) || !t.CorrectionWorkStartedAt.After(t.CorrectionAt)) {
			return ""
		}
		var result comfyPollReceipt
		if json.Unmarshal([]byte(action.Result), &result) != nil || result.JobID == "" || input.Action == "job" && result.JobID != input.JobID {
			return ""
		}
		if action.State == "running" && result.State == "running" {
			return result.JobID
		}
		return ""
	}
	return ""
}

// Persist only a changed receipt. A correction, cancellation or newer job
// arriving during the network read keeps ownership of the task.
func (a *agentControl) finishComfyPoll(snapshot agentTask, jobID string, args json.RawMessage, result string, pollErr error, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.data.Tasks[snapshot.ID]
	if !ok || t.State != "waiting" || !t.Updated.Equal(snapshot.Updated) || t.runningComfyJobID() != jobID {
		return nil
	}
	old := copyTask(t)
	var receipt comfyPollReceipt
	valid := pollErr == nil && json.Unmarshal([]byte(result), &receipt) == nil && receipt.JobID == jobID && receipt.State != ""
	if valid && receipt.State == "running" {
		delay := t.Next.Sub(t.Updated)
		if delay <= 0 {
			delay = time.Minute
		}
		t.Next = now.Add(delay)
	} else {
		state := receipt.State
		if !valid {
			state = "outcome_unknown"
			result = "原作业状态暂时无法核对，请查询同一 job_id。"
			if pollErr != nil {
				result += mcptools.FailureDescription(pollErr)
			}
		}
		entry := taskAction{Tool: "station_comfy", Arguments: string(args), Result: logText(result, 2200), State: state, At: now}
		t.Actions = append(t.Actions, entry)
		if len(t.Actions) > 24 {
			t.Actions = t.Actions[len(t.Actions)-24:]
		}
		t.Next = now
	}
	t.Updated = now
	a.data.Tasks[t.ID] = t
	if err := a.persist(); err != nil {
		a.data.Tasks[t.ID] = old
		return err
	}
	return nil
}
