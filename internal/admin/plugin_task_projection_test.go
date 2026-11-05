package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

func TestPluginTaskPublicProjectionPreservesPrivateRecoveryOrigin(t *testing.T) {
	f, p := trustedAgent(t)
	private := companion.Input{Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, Text: "synthetic-private-recovery-context"}
	task := agentTask{ID: "synthetic-plugin-task", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "waiting", PluginJob: &pluginJobState{Origin: &private, Ticket: plugins.JobTicket{Tool: "synthetic__create", State: "waiting", RemoteID: "synthetic-job"}}}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	result, err := p.callAgent(t.Context(), "agent_tasks", json.RawMessage(`{}`))
	if err != nil || strings.Contains(result, private.Text) || strings.Contains(result, `"origin"`) {
		t.Fatal("private recovery context escaped task projection", err)
	}
	var visible []agentTask
	if json.Unmarshal([]byte(result), &visible) != nil || len(visible) != 1 || visible[0].PluginJob == nil || visible[0].PluginJob.Ticket.RemoteID != "synthetic-job" {
		t.Fatal("public task identity lost")
	}
	stored, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || stored.PluginJob.Origin == nil || stored.PluginJob.Origin.Text != private.Text {
		t.Fatal("projection modified recovery evidence")
	}
}
