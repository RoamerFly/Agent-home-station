package admin

import "encoding/json"

// The most recent job observation is authoritative for a task. A successful
// render poll can stay in_progress for many minutes without yielding new bytes.
func (t agentTask) waitingForComfyJob() bool {
	for i := len(t.Actions) - 1; i >= 0; i-- {
		action := t.Actions[i]
		if action.Tool != "station_comfy" {
			continue
		}
		var input struct {
			Action string `json:"action"`
			JobID  string `json:"job_id"`
		}
		if json.Unmarshal([]byte(action.Arguments), &input) != nil || input.Action != "job" && input.Action != "run" {
			continue
		}
		var result struct {
			JobID string `json:"job_id"`
			State string `json:"state"`
		}
		if json.Unmarshal([]byte(action.Result), &result) != nil || result.JobID == "" || input.Action == "job" && result.JobID != input.JobID {
			return false
		}
		return action.State == "running" && result.State == "running"
	}
	return false
}
