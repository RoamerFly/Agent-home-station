package bot

import (
	"errors"
	"testing"

	"wechat-bot/internal/companion"
)

func TestTaskCompletionCheckpointErrorReachesWorker(t *testing.T) {
	writeErr := errors.New("checkpoint write failed")
	w := &Worker{config: Config{
		AgentTaskDone: func(companion.Input, string) error { return writeErr },
		Log:           func(string) {},
	}}
	w.finishAgentTask(companion.Input{TaskID: "original-task"}, "yielded_to_message")
	if !errors.Is(w.persistErr, writeErr) {
		t.Fatalf("completion error did not stop the scheduler: %v", w.persistErr)
	}
}
