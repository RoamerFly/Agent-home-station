package admin

import (
	"encoding/json"
	"strings"
	"time"

	"wechat-bot/internal/mcptools"
)

type taskAction struct {
	Tool      string    `json:"tool"`
	Arguments string    `json:"arguments"`
	Result    string    `json:"result"`
	State     string    `json:"state"`
	ErrorCode string    `json:"error_code,omitempty"`
	At        time.Time `json:"at"`
}

func (p *permittedTools) rememberTaskAction(name string, args json.RawMessage, result string, err error) {
	if p.trace.TaskID == "" || !p.privileged() || !recordsTaskAction(name) {
		return
	}
	a := p.manager.agent
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.data.Tasks[p.trace.TaskID]
	if !ok || t.Account != p.trace.Account || t.Thread != p.trace.Thread || t.Sender != p.trace.Sender {
		return
	}
	state := "completed"
	var out map[string]any
	_ = json.Unmarshal([]byte(result), &out)
	if value, ok := out["state"].(string); ok {
		state = value
	}
	if out["timed_out"] == true {
		state = "outcome_unknown"
	} else if code, ok := out["exit_code"].(float64); ok && code != 0 {
		state = "failed"
	}
	code := ""
	if err != nil {
		state = "failed"
		result = mcptools.FailureDescription(err)
		_, code = safeToolFailure(err)
	}
	if name == "agent_task" && err == nil {
		var saved struct {
			Task map[string]any `json:"task"`
		}
		if json.Unmarshal([]byte(result), &saved) == nil && saved.Task != nil {
			compact, _ := json.Marshal(map[string]any{"task_id": saved.Task["id"], "state": saved.Task["state"], "next": saved.Task["next"], "checkpoint": saved.Task["checkpoint"]})
			result = string(compact)
		}
	}
	entry := taskAction{Tool: name, Arguments: logText(string(args), 1200), Result: logText(result, 2200), State: state, ErrorCode: code, At: time.Now().UTC()}
	if (name == "station_comfy" || name == "artifact_publish") && err == nil && state != "failed" && !t.CorrectionAt.IsZero() && contains(t.CorrectionTraces, p.trace.TraceID) && entry.At.After(t.CorrectionAt) {
		var input struct {
			Action          string `json:"action"`
			SourceMessageID string `json:"source_message_id"`
		}
		_ = json.Unmarshal(args, &input)
		switch input.Action {
		case "run":
			jobID, _ := out["job_id"].(string)
			if jobID != "" && input.SourceMessageID != "" && input.SourceMessageID == t.CorrectionReferenceImageID {
				t.CorrectionWorkStartedAt = entry.At
			}
		case "attach":
			var prepared struct {
				Media []struct {
					Kind string `json:"kind"`
					ID   string `json:"id"`
				} `json:"media"`
			}
			if json.Unmarshal([]byte(result), &prepared) == nil {
				for _, media := range prepared.Media {
					if media.Kind == t.CorrectionRequiredMediaKind && media.ID != "" && !t.CorrectionWorkStartedAt.IsZero() {
						t.CorrectionMediaPreparedAt = entry.At
						t.CorrectionPreparedMediaID = media.ID
					}
				}
			}
		}
		if name == "artifact_publish" && out["staged_only"] == true && !t.CorrectionWorkStartedAt.IsZero() {
			var prepared struct {
				Media []struct {
					Kind string `json:"kind"`
					ID   string `json:"id"`
				} `json:"media"`
			}
			if json.Unmarshal([]byte(result), &prepared) == nil {
				for _, media := range prepared.Media {
					if media.Kind == t.CorrectionRequiredMediaKind && media.ID != "" {
						t.CorrectionMediaPreparedAt = entry.At
						t.CorrectionPreparedMediaID = media.ID
					}
				}
			}
		}
	}
	if name == "wechat_operation_status" {
		if err != nil {
			return
		} // A read failure cannot erase a previously verified effect.
		var in struct {
			ID string `json:"operation_id"`
		}
		_ = json.Unmarshal(args, &in)
		for i := len(t.Actions) - 1; i >= 0; i-- {
			var old map[string]any
			_ = json.Unmarshal([]byte(t.Actions[i].Result), &old)
			if in.ID != "" && old["operation_id"] == in.ID && t.Actions[i].Tool == "wechat_send_text" {
				t.Actions[i].Result, t.Actions[i].State, t.Actions[i].At = entry.Result, state, entry.At
				a.data.Tasks[t.ID] = t
				if e := a.persist(); e != nil {
					p.manager.log("task_action_evidence_save_failed")
				}
				return
			}
		}
		return
	}
	if name == "server_process" || name == "server_windows_process" {
		var in struct{ Action, ID string }
		_ = json.Unmarshal(args, &in)
		if in.Action == "status" {
			for i := len(t.Actions) - 1; i >= 0; i-- {
				var old map[string]any
				_ = json.Unmarshal([]byte(t.Actions[i].Result), &old)
				if old["id"] == in.ID && t.Actions[i].Tool == name {
					t.Actions[i].Result = entry.Result
					t.Actions[i].State = state
					t.Actions[i].At = entry.At
					a.data.Tasks[t.ID] = t
					if e := a.persist(); e != nil {
						p.manager.log("task_action_evidence_save_failed")
					}
					return
				}
			}
			return // No unbounded polling history.
		}
	}
	t.Actions = append(append([]taskAction{}, t.Actions...), entry)
	if len(t.Actions) > 24 {
		t.Actions = t.Actions[len(t.Actions)-24:]
	}
	a.data.Tasks[t.ID] = t
	if e := a.persist(); e != nil {
		p.manager.log("task_action_evidence_save_failed")
	}
}

func recordsTaskAction(name string) bool {
	switch name {
	case "server_read", "server_list", "server_search", "server_status":
		return false
	case "agent_task", "agent_reminder", "music_local_start", "music_local_resume", "music_rvc_start",
		"wechat_send_text", "wechat_send_media", "wechat_forward_file", "wechat_operation_status", "station_comfy", "artifact_publish":
		return true
	default:
		return strings.HasPrefix(name, "server_")
	}
}
