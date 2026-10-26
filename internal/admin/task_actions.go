package admin

import (
	"encoding/json"
	"strings"
	"time"
)

type taskAction struct {
	Tool      string    `json:"tool"`
	Arguments string    `json:"arguments"`
	Result    string    `json:"result"`
	State     string    `json:"state"`
	At        time.Time `json:"at"`
}

func (p *permittedTools) rememberTaskAction(name string, args json.RawMessage, result string, err error) {
	if p.trace.TaskID == "" || !p.privileged() || !(strings.HasPrefix(name, "server_") || name == "agent_task" || name == "wechat_send_text" || name == "wechat_operation_status") || name == "server_read" || name == "server_list" || name == "server_search" || name == "server_status" {
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
	if err != nil {
		state = "failed"
		result = err.Error()
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
	entry := taskAction{Tool: name, Arguments: logText(string(args), 1200), Result: logText(result, 2200), State: state, At: time.Now().UTC()}
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
	if name == "server_process" {
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
