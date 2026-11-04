package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

type jobFixture struct {
	f                       *webFixture
	p                       *permittedTools
	upstream                *httptest.Server
	creates, polls, cancels atomic.Int32
	done, drop              atomic.Bool
	pollBlock, pollEntered  chan struct{}
}

func newJobFixture(t *testing.T) *jobFixture {
	t.Helper()
	j := &jobFixture{f: setup(t)}
	j.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /jobs":
			j.creates.Add(1)
			if j.drop.Load() {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			w.Write([]byte(`{"task_id":"remote-one","state":"waiting"}`))
		case "GET /jobs/remote-one":
			j.polls.Add(1)
			if j.pollBlock != nil {
				close(j.pollEntered)
				<-j.pollBlock
			}
			if j.done.Load() {
				w.Write([]byte(`{"state":"done","text":"后台工具真实完成输出"}`))
			} else {
				w.Write([]byte(`{"state":"waiting"}`))
			}
		case "POST /jobs/remote-one/cancel":
			j.cancels.Add(1)
			w.Write([]byte(`{"state":"requested"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(j.upstream.Close)
	m := plugins.Manifest{ID: "demo", Name: "合成异步工具", Tools: []plugins.APITool{{Name: "create", Description: "Create synthetic async task", InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"],"additionalProperties":false}`), URL: j.upstream.URL + "/jobs", Method: "POST", BodyTemplate: json.RawMessage(`{"prompt":"{{arg.prompt}}"}`), TextPath: "text", Poll: &plugins.PollSpec{URL: j.upstream.URL + "/jobs/{{result.task_id}}", StatusPath: "state", DoneValues: []string{"done"}, FailedValues: []string{"failed"}, IntervalSeconds: 1}, Job: &plugins.JobSpec{IDPath: "task_id", Cancel: &plugins.CancelSpec{URL: j.upstream.URL + "/jobs/{{result.task_id}}/cancel"}}}}}
	if err := j.f.manager.plugins.save(pluginEntry{Installed: plugins.Installed{Manifest: m, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	j.f.manager.config.Companion.Enabled = true
	j.f.manager.config.Companion.Permissions.Remember = true
	j.f.manager.config.Companion.QuietStart, j.f.manager.config.Companion.QuietEnd = "00:00", "00:00"
	p, _, err := j.f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	j.p = p
	j.p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "alice", TraceID: "request", Mode: "direct", Text: "做一段测试内容"})
	installExternalHTTPTestRuntime(t, j.p)
	return j
}
func (j *jobFixture) start(t *testing.T) agentTask {
	t.Helper()
	if _, err := j.p.Call(context.Background(), "demo__create", json.RawMessage(`{"prompt":"synthetic"}`)); err != nil {
		t.Fatal(err)
	}
	return j.f.manager.agent.list("test", "filehelper")[0]
}
func TestPluginBackgroundPersistsPollsSameJobAndCompletesOnlyAfterDelivery(t *testing.T) {
	j := newJobFixture(t)
	first := j.start(t)
	if first.State != "waiting" || first.PluginJob.Ticket.RemoteID != "remote-one" || j.creates.Load() != 1 {
		t.Fatal(first)
	}
	// Repeating the original tool in the same turn and a process restart must
	// not create a second remote task.
	j.start(t)
	a, err := newAgentControl(j.f.dir)
	if err != nil {
		t.Fatal(err)
	}
	j.f.manager.agent = a
	if in, err := j.f.manager.claimAgentTask(time.Now().Add(2*time.Second), func(string) bool { return false }); err != nil || in != nil {
		t.Fatal(in, err)
	}
	j.done.Store(true)
	in, err := j.f.manager.claimAgentTask(time.Now().Add(5*time.Second), func(string) bool { return false })
	if err != nil || in == nil || in.TaskID != first.ID || in.OriginTraceID != "request" || j.creates.Load() != 1 || j.polls.Load() != 2 {
		t.Fatal(in, err, j.creates.Load(), j.polls.Load())
	}
	p, _, _ := j.f.manager.toolAccess()
	p.SetTrace(*in)
	if _, err := p.Call(context.Background(), "plugin_job_status", json.RawMessage(`{"task_id":"`+first.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Call(context.Background(), "plugin_job_status", json.RawMessage(`{"task_id":"`+first.ID+`"}`)); err == nil {
		t.Fatal("prepared result was staged twice")
	}
	j.f.manager.finishAgentTask(*in, "reply")
	got := j.f.manager.agent.list("test", "filehelper")[0]
	if got.State != "waiting_delivery" || !got.PluginJob.Prepared {
		t.Fatal("prepared equals delivered", got)
	}
	if err := j.f.manager.updateAgentDelivery(*in, bot.TaskDelivery{Trace: in.TraceID, State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if j.f.manager.agent.list("test", "filehelper")[0].State != "completed" {
		t.Fatal("receipt not linked")
	}
}
func TestPluginUnknownCreateNeverReplaysAndCannotResumeCreation(t *testing.T) {
	j := newJobFixture(t)
	j.drop.Store(true)
	if _, err := j.p.Call(context.Background(), "demo__create", json.RawMessage(`{"prompt":"synthetic"}`)); err == nil {
		t.Fatal("unknown create claimed success")
	}
	got := j.f.manager.agent.list("test", "filehelper")[0]
	if got.State != "needs_review" || j.creates.Load() != 1 {
		t.Fatal(got)
	}
	j.p.Call(context.Background(), "demo__create", json.RawMessage(`{"prompt":"synthetic"}`))
	j.p.Call(context.Background(), "demo__create", json.RawMessage(`{"prompt":"changed after uncertain outcome"}`))
	a, err := newAgentControl(j.f.dir)
	if err != nil {
		t.Fatal(err)
	}
	j.f.manager.agent = a
	j.f.login()
	code, _, _ := j.f.request("POST", "/api/agent/tasks/"+got.ID, map[string]string{"action": "resume"}, true, true)
	if code != 409 || j.creates.Load() != 1 {
		t.Fatal("unknown remote action replayed", code, j.creates.Load())
	}
}
func TestPluginCancellationWinsAgainstLatePollAndIsNeverReplayed(t *testing.T) {
	j := newJobFixture(t)
	task := j.start(t)
	j.done.Store(true)
	j.pollBlock, j.pollEntered = make(chan struct{}), make(chan struct{})
	done := make(chan *companion.Input, 1)
	go func() { in, _ := j.f.manager.claimPluginTask(task, time.Now()); done <- in }()
	<-j.pollEntered
	stopped, err := j.f.manager.cancelPluginTask(context.Background(), task)
	if err != nil || stopped.State != "cancelled" {
		t.Fatal(stopped, err)
	}
	close(j.pollBlock)
	if in := <-done; in != nil {
		t.Fatal("late poll resurrected cancelled delivery", in)
	}
	got := j.f.manager.agent.list("test", "filehelper")[0]
	j.f.manager.cancelPluginTask(context.Background(), got)
	if got.State != "cancelled" || j.cancels.Load() != 1 || j.creates.Load() != 1 {
		t.Fatal(got, j.cancels.Load())
	}
	if j.f.manager.agentTaskActive(companion.Input{Account: "test", Thread: "filehelper", TaskID: task.ID}) {
		t.Fatal("cancelled task still active")
	}
}
func TestPluginJobScopeAndConfigChangeDoNotContactNewEndpoint(t *testing.T) {
	j := newJobFixture(t)
	task := j.start(t)
	for _, in := range []companion.Input{{Account: "test", Thread: "filehelper", Sender: "bob"}, {Account: "test", Thread: "other@chatroom", Sender: "alice"}} {
		j.p.SetTrace(in)
		if _, err := j.p.callPluginJob(context.Background(), "plugin_job_cancel", json.RawMessage(`{"task_id":"`+task.ID+`"}`)); err == nil {
			t.Fatal("scope bypass")
		}
	}
	entry, _ := j.f.manager.plugins.get("demo")
	entry.Manifest.Tools[0].Poll.URL = j.upstream.URL + "/changed/{{result.task_id}}"
	j.f.manager.plugins.save(entry)
	if in, err := j.f.manager.claimPluginTask(task, time.Now()); err != nil || in != nil {
		t.Fatal(in, err)
	}
	if j.f.manager.agent.list("test", "filehelper")[0].State != "needs_review" || j.polls.Load() != 0 {
		t.Fatal("old task called new descriptor")
	}
}

func TestPreparedPluginResultIsNotAutomaticallyRestagedAfterInterruptedDelivery(t *testing.T) {
	j := newJobFixture(t)
	first := j.start(t)
	j.done.Store(true)
	in, err := j.f.manager.claimAgentTask(time.Now().Add(2*time.Second), func(string) bool { return false })
	if err != nil || in == nil {
		t.Fatal(in, err)
	}
	p, _, _ := j.f.manager.toolAccess()
	p.SetTrace(*in)
	if _, err := p.Call(context.Background(), "plugin_job_status", json.RawMessage(`{"task_id":"`+first.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	j.f.manager.finishAgentTask(*in, "silent")
	got := j.f.manager.agent.list("test", "filehelper")[0]
	if got.State != "needs_review" {
		t.Fatal("prepared result auto-replayed", got)
	}
	j.f.login()
	code, _, _ := j.f.request("POST", "/api/agent/tasks/"+first.ID, map[string]string{"action": "resume"}, true, true)
	if code != 409 || j.creates.Load() != 1 {
		t.Fatal("resume replayed staging", code)
	}
}

func TestPluginDeadlineStopsPollingWithoutStartingReplacement(t *testing.T) {
	j := newJobFixture(t)
	task := j.start(t)
	task.PluginJob.Deadline = time.Now().Add(-time.Second)
	if _, err := j.f.manager.agent.updateSnapshot(task); err != nil {
		t.Fatal(err)
	}
	task = j.f.manager.agent.list("test", "filehelper")[0]
	if in, err := j.f.manager.claimPluginTask(task, time.Now()); err != nil || in != nil {
		t.Fatal(in, err)
	}
	if got := j.f.manager.agent.list("test", "filehelper")[0]; got.State != "needs_review" || j.polls.Load() != 0 || j.creates.Load() != 1 {
		t.Fatal(got)
	}
}
