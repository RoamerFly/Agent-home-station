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

// The fixture simulates only package response interpretation. All actual HTTP,
// admission, task CAS, source checks and private checkpoint work use production
// host code; the separate LPAC contract executes the real Python package.
func syntheticExternalJobReply(args externalHTTPArguments, reply externalHTTPObservation) (json.RawMessage, error) {
	if args.Ticket == nil {
		return nil, errors.New("synthetic ticket missing")
	}
	ticket := *args.Ticket
	result := externalJobWireResult{Observation: reply, Ticket: ticket}
	if args.Phase == "cancel" {
		result.CancelAck = true
		return json.Marshal(result)
	}
	var protocol plugins.ExternalJobProtocol
	if json.Unmarshal(args.Protocol, &protocol) != nil {
		return nil, errors.New("synthetic protocol invalid")
	}
	data, err := base64.StdEncoding.DecodeString(reply.BodyBase64)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object any
	if decoder.Decode(&object) != nil {
		return nil, errors.New("synthetic response invalid")
	}
	if args.Phase == "start" {
		value := externalHTTPValue(object, protocol.Tool.Job.IDPath)
		if value != nil {
			ticket.RemoteID = externalHTTPText(value)[0]
		}
	}
	status := externalHTTPText(externalHTTPValue(object, protocol.Tool.Poll.StatusPath))[0]
	ticket.State = "waiting"
	ticket.Output = plugins.Output{}
	for _, failed := range protocol.Tool.Poll.FailedValues {
		if failed == status {
			ticket.State = "failed"
		}
	}
	for _, done := range protocol.Tool.Poll.DoneValues {
		if done == status && ticket.State != "failed" {
			ticket.State = "completed"
			path := protocol.Tool.TextPath
			if protocol.Tool.Poll.TextPath != "" {
				path = protocol.Tool.Poll.TextPath
			}
			ticket.Output.Text = externalHTTPText(externalHTTPValue(object, path))[0]
		}
	}
	if args.Phase == "start" && (ticket.State == "completed" && ticket.Output.Text == "" && len(ticket.Output.Media) == 0 || status == "" && validExternalRemoteID(ticket.RemoteID)) {
		ticket.State = "waiting"
		result.KnownRemoteOnly = true
	}
	result.Ticket = ticket
	return json.Marshal(result)
}

func TestExternalJobUsesOriginalActorAndRejectsChangedCredentials(t *testing.T) {
	j := newJobFixture(t)
	j.p.trace.ExpectedVoiceID = "original-voice-proof"
	task := j.start(t)
	if task.PluginJob.Origin == nil || task.PluginJob.Origin.ExpectedVoiceID != "original-voice-proof" || task.PluginJob.LegacyConfigurationSHA256 == "" {
		t.Fatal("original request proof was not persisted")
	}
	j.p.trace.Sender = "later-speaker"
	trace, err := j.f.manager.externalJobTrace(task)
	if err != nil || trace.Sender != "alice" || trace.TraceID != "request" || trace.TaskID != task.ID || trace.ExpectedVoiceID != "original-voice-proof" {
		t.Fatal("background task borrowed a later speaker", trace, err)
	}
	entry, _ := j.f.manager.plugins.get("demo")
	entry.Secrets = map[string]string{"changed": "new-credential"}
	if err = j.f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	if _, err = j.f.manager.callExternalJobPhase(t.Context(), task, "poll", json.RawMessage(`{}`)); !errors.Is(err, plugins.ErrJobConfigChanged) || j.polls.Load() != 0 {
		t.Fatal("old remote ID used changed credentials", err, j.polls.Load())
	}
}

func TestExternalJobPollRoundCheckpointDoesNotRepeatHTTP(t *testing.T) {
	j := newJobFixture(t)
	task := j.start(t)
	for range 2 {
		ticket, err := j.f.manager.callExternalJobPhase(t.Context(), task, "poll", json.RawMessage(`{}`))
		if err != nil || ticket.RemoteID != "remote-one" || ticket.State != "waiting" {
			t.Fatal(ticket, err)
		}
	}
	if j.polls.Load() != 1 || j.creates.Load() != 1 {
		t.Fatal("same persisted poll round reissued HTTP", j.polls.Load(), j.creates.Load())
	}
}

func TestExternalJobContinuationKeepsOriginalVoiceEvidenceRequirements(t *testing.T) {
	j := newJobFixture(t)
	j.p.trace.ExpectedVoiceID, j.p.trace.ExpectedVoiceProfile = "original-voice", "original-profile"
	task := j.start(t)
	j.done.Store(true)
	in, err := j.f.manager.claimPluginTask(task, time.Now().Add(2*time.Second))
	if err != nil || in == nil || in.ExpectedVoiceID != "original-voice" || in.ExpectedVoiceProfile != "original-profile" || in.Sender != "alice" {
		t.Fatal("continuation dropped voice proof requirements or changed actor", in, err)
	}
}

func TestExternalJobKnownRemoteIDSurvivesInvalidImmediateCompletionOutput(t *testing.T) {
	for _, initialState := range []string{"done", "missing", "null"} {
		t.Run(initialState, func(t *testing.T) {
			j := newJobFixture(t)
			var creates, polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "POST" {
					creates.Add(1)
					response := map[string]any{"task_id": "known-remote"}
					if initialState == "done" {
						response["state"] = "done"
					} else if initialState == "null" {
						response["state"] = nil
					}
					_ = json.NewEncoder(w).Encode(response)
				} else {
					polls.Add(1)
					_, _ = w.Write([]byte(`{"state":"done","text":"Recovered original remote output"}`))
				}
			}))
			defer server.Close()
			entry, _ := j.f.manager.plugins.get("demo")
			entry.Manifest.Tools[0].URL = server.URL + "/jobs"
			entry.Manifest.Tools[0].Poll.URL = server.URL + "/jobs/{{result.task_id}}"
			if err := j.f.manager.plugins.save(entry); err != nil {
				t.Fatal(err)
			}
			runtime, err := plugins.New(j.f.manager.plugins.installed())
			if err != nil {
				t.Fatal(err)
			}
			j.p.runtime = runtime
			task := j.start(t)
			if task.PluginJob.Ticket.RemoteID != "known-remote" || task.PluginJob.Ticket.State != "waiting" || creates.Load() != 1 {
				t.Fatal("known creation identity was lost with invalid output", task)
			}
			ticket, err := j.f.manager.callExternalJobPhase(t.Context(), task, "poll", json.RawMessage(`{}`))
			if err != nil || ticket.State != "completed" || ticket.Output.Text != "Recovered original remote output" || creates.Load() != 1 || polls.Load() != 1 {
				t.Fatal("known remote identity was recreated instead of queried", ticket, err, creates.Load(), polls.Load())
			}
		})
	}

}

func TestExternalJobMissingInstalledPackageRetainsOriginalTask(t *testing.T) {
	j := newJobFixture(t)
	task := j.start(t)
	store, err := j.f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	i, err := store.Installed(t.Context(), externalHTTPPluginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(t.Context(), externalHTTPPluginID, i.Revision, false); err != nil {
		t.Fatal(err)
	}
	if in, err := j.f.manager.claimPluginTask(task, time.Now()); err != nil || in != nil {
		t.Fatal(in, err)
	}
	got := j.f.manager.agent.list("test", "filehelper")[0]
	if got.PluginJob.Ticket.RemoteID != "remote-one" || got.State != "waiting" || j.polls.Load() != 0 || j.creates.Load() != 1 {
		t.Fatal("missing package fell back or discarded the original task", got)
	}
	if !strings.Contains(got.LastResult, "同一任务") {
		t.Fatal(got.LastResult)
	}
}

func TestExternalLegacyJobWithoutProofIsRetainedForReviewWithoutHTTP(t *testing.T) {
	for _, sourceMissing := range []bool{false, true} {
		t.Run(map[bool]string{false: "configuration proof missing", true: "source proof missing"}[sourceMissing], func(t *testing.T) {
			j := newJobFixture(t)
			ticket, err := j.p.runtime.NewJobTicket("demo__create")
			if err != nil {
				t.Fatal(err)
			}
			ticket.RemoteID, ticket.State = "remote-one", "waiting"
			task := agentTask{ID: "legacy-plugin-task", Account: "test", Thread: "filehelper", Sender: "alice", OriginTrace: "legacy-request", State: "waiting", PluginJob: &pluginJobState{Ticket: ticket, Deadline: time.Now().Add(time.Hour), IntervalSeconds: 1, MaxPolls: 10}}
			if sourceMissing {
				_, hash, err := j.f.manager.externalHTTPBridge()
				if err != nil {
					t.Fatal(err)
				}
				task.PluginJob.LegacyConfigurationSHA256 = hash
			}
			if err := j.f.manager.agent.task(task); err != nil {
				t.Fatal(err)
			}
			task = j.f.manager.agent.list("test", "filehelper")[0]
			if in, err := j.f.manager.claimPluginTask(task, time.Now()); err != nil || in != nil {
				t.Fatal(in, err)
			}
			got := j.f.manager.agent.list("test", "filehelper")[0]
			marker := "legacy_configuration_proof_missing"
			if sourceMissing {
				marker = "legacy_source_proof_missing"
			}
			if got.State != "needs_review" || !strings.Contains(got.LastResult, marker) || got.PluginJob.Ticket.RemoteID != "remote-one" || got.PluginJob.Origin != nil || j.creates.Load()+j.polls.Load()+j.cancels.Load() != 0 {
				t.Fatal("legacy task was erased, rebound, or replayed", got)
			}
		})
	}
}

func TestLiveExternalJobInstalledLPACPreservesStartPollAndCompletedCancel(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated external job phases with a local synthetic provider")
	}
	j := newJobFixture(t)
	store, err := j.f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	_ = j.f.manager.packageRuntime.Close()
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	j.f.manager.packageRuntime, j.f.manager.packageRuntimeReady = controller, true
	t.Cleanup(func() { _ = controller.Close() })
	task := j.start(t)
	if task.PluginJob.Ticket.RemoteID != "remote-one" || j.creates.Load() != 1 {
		t.Fatal("isolated creation lost actual remote identity", task)
	}
	j.done.Store(true)
	ticket, err := j.f.manager.callExternalJobPhase(t.Context(), task, "poll", json.RawMessage(`{}`))
	if err != nil || ticket.State != "completed" || ticket.Output.Text != "后台插件真实完成输出" || j.polls.Load() != 1 {
		t.Fatal("isolated poll lost actual provider result", ticket, err)
	}
	task.PluginJob.Ticket = ticket
	if ok, err := j.f.manager.agent.updateSnapshot(task); err != nil || !ok {
		t.Fatal("could not retain completed remote ticket", ok, err)
	}
	task = j.f.manager.agent.list("test", "filehelper")[0]
	stopped, err := j.f.manager.cancelPluginTask(t.Context(), task)
	if err != nil || stopped.State != "cancelled" || stopped.PluginJob.CancelState != "requested" || j.cancels.Load() != 1 {
		t.Fatal("cancel claim could not cancel a completed remote job once", stopped, err)
	}
	if _, err := j.f.manager.cancelPluginTask(t.Context(), stopped); err != nil || j.cancels.Load() != 1 || j.creates.Load() != 1 {
		t.Fatal("isolated cancellation replayed a remote operation", err)
	}
}
