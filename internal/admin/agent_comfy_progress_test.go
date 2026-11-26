package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunningComfyJobDoesNotExhaustTaskNoProgress(t *testing.T) {
	const jobID = "synthetic-render"
	task := agentTask{State: "waiting", Goal: "render a video", Checkpoint: "poll the original job"}
	task.Actions = []taskAction{{Tool: "station_comfy", Arguments: `{"action":"job","job_id":"` + jobID + `"}`, Result: `{"job_id":"` + jobID + `","state":"running","backend_state":"in_progress"}`, State: "running"}}
	for run := 1; run <= repeatedNoProgressLimit+2; run++ {
		task.Runs = run
		if task.stopRepeatedNoProgress("saved_checkpoint") || task.State != "waiting" || task.NoProgressRuns != 0 {
			t.Fatalf("active render was stopped on run %d: %+v", run, task)
		}
	}
	task.Actions = append(task.Actions, taskAction{Tool: "station_comfy", Arguments: `{"action":"job","job_id":"` + jobID + `"}`, Result: `{"job_id":"` + jobID + `","state":"failed"}`, State: "failed"})
	for run := repeatedNoProgressLimit + 3; run < repeatedNoProgressLimit*2+3; run++ {
		task.Runs = run
		stopped := task.stopRepeatedNoProgress("saved_checkpoint")
		if run == repeatedNoProgressLimit*2+2 && (!stopped || task.State != "stopped") {
			t.Fatalf("terminal job did not reenable no-progress guard: %+v", task)
		}
	}
}

func TestSchedulerPollsOriginalComfyJobUntilReady(t *testing.T) {
	f, tools := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	now := time.Now().UTC()
	id := strings.Repeat("a", 64)
	result := func(state string) string {
		return `{"job_id":"` + id + `","state":"` + state + `"}`
	}
	task := agentTask{ID: "render", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", OwnerRequested: true,
		State: "waiting", Next: now}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	tools.trace.TaskID = task.ID
	runArgs, _ := json.Marshal(map[string]any{"action": "run", "inputs": map[string]string{"prompt": strings.Repeat("x", 2000)}})
	tools.rememberTaskAction("station_comfy", runArgs, result("running"), nil)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" {
			http.SetCookie(w, &http.Cookie{Name: "station", Value: "synthetic"})
			w.Write([]byte(`{"authenticated":true,"csrf":"test"}`))
			return
		}
		var req struct {
			Action    string
			JobID     string `json:"job_id"`
			JobAction string `json:"job_action"`
		}
		if r.URL.Path != "/api/local/project-tools/comfyui/action" || json.NewDecoder(r.Body).Decode(&req) != nil ||
			req.Action != "job" || req.JobAction != "status" || req.JobID != id {
			t.Errorf("unexpected workstation request: %s %+v", r.URL.Path, req)
			http.Error(w, "unexpected request", 400)
			return
		}
		state := "running"
		if polls.Add(1) == 6 {
			state = "completed"
		}
		w.Write([]byte(`{"result":` + result(state) + `}`))
	}))
	defer server.Close()
	t.Setenv("AGENT_STATION_URL", server.URL)
	for n := 1; n <= 6; n++ {
		in, err := f.manager.claimAgentTask(now.Add(time.Duration(n)*time.Minute), func(string) bool { return false })
		if in != nil || err != nil || polls.Load() != int32(n) {
			t.Fatalf("poll %d: unexpected generation, failure, or poll count %d: %v %v", n, polls.Load(), in, err)
		}
	}
	in, err := f.manager.claimAgentTask(now.Add(7*time.Minute), func(string) bool { return false })
	if err != nil || in == nil || in.TaskID != task.ID || polls.Load() != 6 || !strings.Contains(in.Text, "completed") {
		t.Fatalf("completed job did not resume with its receipt: %v %v", in, err)
	}
}

func TestComfyPollCannotOverwriteCorrection(t *testing.T) {
	a, err := newAgentControl(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	t0 := agentTask{ID: "render", State: "waiting", Updated: now, Actions: []taskAction{{Tool: "station_comfy", Arguments: `{"action":"run"}`, Result: `{"job_id":"old","state":"running"}`, State: "running", At: now}}}
	corrected := t0
	corrected.CorrectionAt = now.Add(time.Second)
	corrected.Updated = corrected.CorrectionAt
	a.data.Tasks[t0.ID] = corrected
	if corrected.runningComfyJobID() != "" {
		t.Fatal("new correction waited on old job")
	}
	err = a.finishComfyPoll(t0, "old", nil, `{"job_id":"old","state":"completed"}`, nil, now.Add(2*time.Second))
	if err != nil || len(a.data.Tasks[t0.ID].Actions) != 1 {
		t.Fatalf("stale receipt was accepted: %v", err)
	}
}

func TestComfyPollReleasesTerminalOrUnverifiedJobs(t *testing.T) {
	a, err := newAgentControl(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ result, state string }{
		{`{"job_id":"original","state":"failed"}`, "failed"},
		{`{"job_id":"original","state":"cancelled"}`, "cancelled"},
		{`{"job_id":"other","state":"completed"}`, "outcome_unknown"},
	} {
		now := time.Now().UTC()
		task := agentTask{ID: "render", State: "waiting", Updated: now, Actions: []taskAction{{Tool: "station_comfy", Arguments: `{"action":"run"}`, Result: `{"job_id":"original","state":"running"}`, State: "running"}}}
		a.data.Tasks[task.ID] = task
		if err := a.finishComfyPoll(task, "original", json.RawMessage(`{"action":"job","job_id":"original"}`), test.result, nil, now); err != nil {
			t.Fatal(err)
		}
		got := a.data.Tasks[task.ID]
		if got.runningComfyJobID() != "" || !got.Next.Equal(now) || got.Actions[len(got.Actions)-1].State != test.state {
			t.Fatalf("result %s did not release task correctly", test.state)
		}
	}
}
