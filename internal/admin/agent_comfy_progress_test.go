package admin

import "testing"

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
