package bot

import (
	"context"
	"time"
	"wechat-bot/internal/companion"
)

func (w *Worker) taskActive(in companion.Input) bool {
	return in.TaskID == "" || w.config.AgentTaskActive == nil || w.config.AgentTaskActive(in)
}

func (w *Worker) tickAgentTask(ctx context.Context) bool {
	c, ok := w.model.(*companion.Engine)
	if !ok || w.runtime == nil || w.config.AgentTask == nil || w.draining() {
		return false
	}
	in, err := w.config.AgentTask(time.Now(), func(thread string) bool {
		return w.generation != nil && w.generation.jobs[thread] != nil || w.pendingConversationThread(thread)
	})
	if err != nil {
		w.config.Log("agent_task_schedule_failed")
		return false
	}
	if in == nil {
		return false
	}
	if !w.allowed[in.Thread] {
		return false
	}
	in.PersonaOverride = w.config.GroupRules[in.Thread].Persona
	w.activity(Record{Event: "agent_task_started", Category: "decision", ThreadID: in.Thread, MessageID: in.TraceID, Status: "running", Reason: "persistent_task_continuation", Origin: "agent"})
	w.respondCharacter(ctx, c, *in)
	if w.generation == nil && w.config.AgentTaskDone != nil {
		w.config.AgentTaskDone(*in, "synchronous_round_finished")
	}
	return true
}
