package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
)

func syntheticExternalPollReply(args externalHTTPArguments, reply externalHTTPObservation) (json.RawMessage, error) {
	if args.Ticket == nil {
		return nil, errors.New("missing poll ticket")
	}
	ticket := *args.Ticket
	ticket.RemoteID = ""
	ticket.Output = plugins.Output{}
	var protocol plugins.ExternalJobProtocol
	if json.Unmarshal(args.Protocol, &protocol) != nil || protocol.Tool.Poll == nil {
		return nil, errors.New("poll protocol")
	}
	result := externalPollWireResult{Observation: reply, Ticket: ticket}
	if reply.BinaryRef != nil {
		result.Ticket.State = "completed"
		result.Output = &externalHTTPWireOutput{Media: []externalHTTPWireMedia{{MediaResult: plugins.MediaResult{Kind: protocol.Tool.MediaKind}, HostBinaryRef: reply.BinaryRef.SHA256}}}
		return json.Marshal(result)
	}
	data, _ := base64.StdEncoding.DecodeString(reply.BodyBase64)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, errors.New("poll response")
	}
	status := externalHTTPText(externalHTTPValue(value, protocol.Tool.Poll.StatusPath))[0]
	result.Ticket.State = "waiting"
	if containsMCPName(protocol.Tool.Poll.FailedValues, status) {
		result.Ticket.State = "failed"
	} else if containsMCPName(protocol.Tool.Poll.DoneValues, status) {
		result.Ticket.State = "completed"
		selected := protocol.Tool
		if selected.Poll.TextPath != "" {
			selected.TextPath = selected.Poll.TextPath
		}
		if selected.Poll.MediaURLPath != "" {
			selected.MediaURLPath = selected.Poll.MediaURLPath
		}
		output := externalHTTPWireOutput{Text: externalHTTPText(externalHTTPValue(value, selected.TextPath))[0]}
		if selected.MediaURLPath != "" {
			url, _ := externalHTTPValue(value, selected.MediaURLPath).(string)
			output.Media = []externalHTTPWireMedia{{MediaResult: plugins.MediaResult{Kind: selected.MediaKind, URL: url}}}
		}
		result.Output = &output
	}
	return json.Marshal(result)
}

type pollFixture struct {
	f                  *webFixture
	p                  *permittedTools
	server             *httptest.Server
	creates, queries   atomic.Int32
	done, drop, binary atomic.Bool
	entered, release   chan struct{}
}

func newPollFixture(t *testing.T) *pollFixture {
	t.Helper()
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	x := &pollFixture{f: f, p: p}
	x.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-poll-secret" {
			t.Error("missing private credential")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /create":
			x.creates.Add(1)
			if x.drop.Load() {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			if x.binary.Load() {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(bytes.Repeat([]byte{0x7f}, 25<<20))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"state":"waiting","tracking":{"batch":"batch-fixed","token":"token-fixed"},"seed":18446744073709551615}`))
		case "GET /poll/batch-fixed/token-fixed/18446744073709551615":
			x.queries.Add(1)
			if r.URL.Query().Get("fixed") != "query" || r.Header.Get("X-Arg") != "synthetic" {
				t.Error("original result/args template changed", r.URL.RawQuery)
			}
			if x.entered != nil {
				close(x.entered)
				select {
				case <-x.release:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			if x.done.Load() {
				_, _ = w.Write([]byte(`{"state":"done","result":{"text":"actual polled output"}}`))
			} else {
				_, _ = w.Write([]byte(`{"state":"waiting"}`))
			}
		default:
			t.Error("unexpected path", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(x.server.Close)
	entry := pluginEntry{Installed: plugins.Installed{Enabled: true, Secrets: map[string]string{"TOKEN": "synthetic-poll-secret"}, Manifest: plugins.Manifest{ID: "poll_only", Name: "Synthetic poll", Tools: []plugins.APITool{{Name: "create", Description: "Synthetic no-ID poll", InputSchema: json.RawMessage(`{"type":"object"}`), URL: x.server.URL + "/create", Method: "POST", TimeoutSeconds: 8, Headers: map[string]string{"Authorization": "Bearer {{secret.TOKEN}}"}, TextPath: "unused", MediaKind: "file", Poll: &plugins.PollSpec{URL: x.server.URL + "/poll/{{result.tracking.batch}}/{{result.tracking.token}}/{{result.seed}}?fixed=query", Headers: map[string]string{"Authorization": "Bearer {{secret.TOKEN}}", "X-Arg": "{{arg.prompt}}"}, StatusPath: "state", DoneValues: []string{"done"}, FailedValues: []string{"failed"}, TextPath: "result.text", IntervalSeconds: 1, MaxAttempts: 4}}}}}}
	if err := f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	access, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	access.SetTrace(p.trace)
	x.p = access
	t.Cleanup(func() { _ = access.hub.Close() })
	store := installExternalHTTPFixture(t, access)
	controller, err := pluginruntime.NewController(store, &externalSyntheticExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	t.Cleanup(func() { _ = controller.Close() })
	return x
}
func (x *pollFixture) start(t *testing.T) agentTask {
	t.Helper()
	_, err := x.p.Call(t.Context(), "poll_only__create", json.RawMessage(`{"prompt":"synthetic"}`))
	if err != nil {
		t.Fatal(err)
	}
	tasks := x.f.manager.agent.list(x.p.trace.Account, x.p.trace.Thread)
	if len(tasks) != 1 {
		t.Fatal(tasks)
	}
	return tasks[0]
}
func TestExternalPollPersistsInitialResponseAndOriginalScopeAcrossRestart(t *testing.T) {
	x := newPollFixture(t)
	task := x.start(t)
	if task.PluginJob.PollState == nil || task.PluginJob.PollState.Initial == nil || task.PluginJob.Ticket.RemoteID != "" || task.PluginJob.Ticket.State != "waiting" {
		t.Fatal(task)
	}
	x.start(t)
	if x.creates.Load() != 1 {
		t.Fatal(x.creates.Load())
	}
	a, err := newAgentControl(x.f.dir)
	if err != nil {
		t.Fatal(err)
	}
	x.f.manager.agent = a
	if _, _, e := x.f.manager.callExternalPollPhase(t.Context(), task, "query"); e != nil {
		t.Fatal("same-round query", e)
	}
	if in, err := x.f.manager.claimAgentTask(time.Now().Add(2*time.Second), func(string) bool { return false }); err != nil || in != nil {
		t.Fatal(in, err)
	}
	x.done.Store(true)
	in, err := x.f.manager.claimAgentTask(time.Now().Add(5*time.Second), func(string) bool { return false })
	if err != nil || in == nil || in.TaskID != task.ID || in.Sender != task.Sender || in.OriginTraceID != task.OriginTrace {
		t.Fatal(in, err, x.queries.Load(), x.f.manager.agent.list(task.Account, task.Thread))
	}
	x.p.SetTrace(*in)
	output, err := x.p.Call(t.Context(), "plugin_job_status", json.RawMessage(`{"task_id":"`+task.ID+`"}`))
	if err != nil || !strings.Contains(output, "actual polled output") || x.creates.Load() != 1 || x.queries.Load() != 2 {
		t.Fatal(output, err, x.creates.Load(), x.queries.Load())
	}
}
func TestExternalPollUnknownStartIsRetainedWithoutReplay(t *testing.T) {
	x := newPollFixture(t)
	x.drop.Store(true)
	for i := 0; i < 2; i++ {
		_, err := x.p.Call(t.Context(), "poll_only__create", json.RawMessage(`{"prompt":"synthetic"}`))
		if i == 0 && err == nil {
			t.Fatal("unknown start accepted")
		}
	}
	tasks := x.f.manager.agent.list(x.p.trace.Account, x.p.trace.Thread)
	if len(tasks) != 1 || tasks[0].State != "needs_review" || tasks[0].PluginJob.Ticket.RemoteID != "" || x.creates.Load() != 1 || x.queries.Load() != 0 {
		t.Fatal(tasks, x.creates.Load())
	}
}
func TestExternalPollCancelIsLocalOnlyAndLateQueryCannotResurrect(t *testing.T) {
	x := newPollFixture(t)
	task := x.start(t)
	x.done.Store(true)
	x.entered, x.release = make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = x.f.manager.claimAgentTask(time.Now().Add(2*time.Second), func(string) bool { return false })
	}()
	select {
	case <-x.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("query not entered")
	}
	cancelled, err := x.f.manager.cancelPluginTask(t.Context(), task)
	if err != nil || cancelled.State != "cancelled" || cancelled.PluginJob.CancelState != "local_only" {
		t.Fatal(cancelled, err)
	}
	close(x.release)
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("late query did not finish")
	}
	current, _ := x.f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if current.State != "cancelled" || current.PluginJob.CancelState != "local_only" || current.PluginJob.Ticket.State == "completed" || x.creates.Load() != 1 || x.queries.Load() != 1 {
		t.Fatal(current, x.creates.Load(), x.queries.Load())
	}
}
func TestExternalPollInitialBytesAndChangedCredentialsBlockQueries(t *testing.T) {
	x := newPollFixture(t)
	task := x.start(t)
	ref := task.PluginJob.PollState.Initial
	path := filepath.Join(x.f.manager.privateWorkspaceFor(task.Account), "external-http-replies", ref.JobID, "reply.bin")
	if err := os.WriteFile(path, []byte(`{"state":"waiting","tracking":{"batch":"forged"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := x.f.manager.callExternalPollPhase(t.Context(), task, "query"); err == nil || x.queries.Load() != 0 {
		t.Fatal("tampered initial accepted", err)
	}
	entry, _ := x.f.manager.plugins.get("poll_only")
	entry.Secrets["TOKEN"] = "changed"
	if err := x.f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	if _, _, err := x.f.manager.callExternalPollPhase(t.Context(), task, "query"); !errors.Is(err, plugins.ErrJobConfigChanged) || x.queries.Load() != 0 {
		t.Fatal(err, x.queries.Load())
	}
}
func TestExternalPollBinaryImmediateCompletionUsesPrivateInitialReference(t *testing.T) {
	x := newPollFixture(t)
	x.binary.Store(true)
	task := x.start(t)
	if task.PluginJob.Ticket.State != "completed" || task.PluginJob.PollState.Initial == nil || task.PluginJob.Ticket.RemoteID != "" {
		t.Fatal(task)
	}
	in, err := x.f.manager.claimAgentTask(time.Now().Add(2*time.Second), func(string) bool { return false })
	if err != nil || in == nil {
		t.Fatal(in, err)
	}
	current, _ := x.f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	output, _, err := x.f.manager.externalPollCachedOutput(t.Context(), current)
	if err != nil || len(output.Media) != 1 || len(output.Media[0].Data) != 25<<20 || output.Media[0].Data[0] != 0x7f || x.creates.Load() != 1 || x.queries.Load() != 0 {
		t.Fatal("private binary not restored", err, len(output.Media))
	}
	page, _ := x.f.manager.packageStore()
	jobs, _ := page.Jobs(t.Context(), externalHTTPPluginID, 0, 10)
	for _, job := range jobs.Items {
		if len(job.Result) > 8192 || len(job.Arguments) > 8192 {
			t.Fatal("binary escaped into checkpoint/public args")
		}
	}
}
func TestLiveExternalPollInstalledLPACKeepsInitialTemplateAndBinaryReferences(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in installed poll LPAC using only synthetic local provider")
	}
	x := newPollFixture(t)
	store, _ := x.f.manager.packageStore()
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	x.f.manager.packageRuntime, x.f.manager.packageRuntimeReady = controller, true
	t.Cleanup(func() { _ = controller.Close() })
	task := x.start(t)
	x.done.Store(true)
	in, err := x.f.manager.claimAgentTask(time.Now().Add(2*time.Second), func(string) bool { return false })
	if err != nil || in == nil || in.TaskID != task.ID {
		t.Fatal(in, err)
	}
	current, _ := x.f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if current.PluginJob.Ticket.Output.Text != "actual polled output" || current.PluginJob.Ticket.RemoteID != "" || x.creates.Load() != 1 || x.queries.Load() != 1 {
		t.Fatal(current, x.creates.Load(), x.queries.Load())
	}
	x.binary.Store(true)
	x.p.trace.TraceID = "binary-operation"
	binaryTask := x.startWithID(t)
	in, err = x.f.manager.claimAgentTask(time.Now().Add(3*time.Second), func(string) bool { return false })
	if err != nil || in == nil || in.TaskID != binaryTask.ID {
		t.Fatal(in, err)
	}
	current, _ = x.f.manager.agent.lookupTask(binaryTask.ID, binaryTask.Account, binaryTask.Thread)
	output, _, err := x.f.manager.externalPollCachedOutput(t.Context(), current)
	if err != nil || len(output.Media) != 1 || len(output.Media[0].Data) != 25<<20 || x.creates.Load() != 2 || x.queries.Load() != 1 {
		t.Fatal("isolated binary reference not restored", err)
	}
}
func (x *pollFixture) startWithID(t *testing.T) agentTask {
	t.Helper()
	text, err := x.p.Call(t.Context(), "poll_only__create", json.RawMessage(`{"prompt":"synthetic"}`))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal([]byte(text), &summary) != nil {
		t.Fatal(text)
	}
	task, ok := x.f.manager.agent.lookupTask(summary.TaskID, x.p.trace.Account, x.p.trace.Thread)
	if !ok {
		t.Fatal("task not saved")
	}
	return task
}
func TestExternalPollDisabledPackageRetainsTaskAndDoesNotFallBack(t *testing.T) {
	x := newPollFixture(t)
	task := x.start(t)
	store, _ := x.f.manager.packageStore()
	installed, err := store.Installed(t.Context(), externalHTTPPluginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(t.Context(), externalHTTPPluginID, installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	if in, err := x.f.manager.claimAgentTask(time.Now().Add(2*time.Second), func(string) bool { return false }); err != nil || in != nil {
		t.Fatal(in, err)
	}
	current, _ := x.f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if current.PluginJob.Polls != 0 || current.PluginJob.PollState.Initial == nil || current.State != "waiting" {
		t.Fatal(current)
	}
	x.p.trace.TraceID = "disabled-new-operation"
	if _, err = x.p.Call(t.Context(), "poll_only__create", json.RawMessage(`{"prompt":"synthetic"}`)); err == nil {
		t.Fatal("disabled package fell back")
	}
	if x.creates.Load() != 1 || x.queries.Load() != 0 {
		t.Fatal(x.creates.Load(), x.queries.Load())
	}
}
