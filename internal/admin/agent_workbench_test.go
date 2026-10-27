package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestAgentLegacySmallBudgetDoesNotLimitAutonomousRuntime(t *testing.T) {
	f := setup(t)
	c := f.manager.agent.config()
	c.MaxSteps = 1
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	p, steps, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	defer p.hub.Close()
	if steps != agentTurnToolLimit || steps <= c.MaxSteps {
		t.Fatal("legacy UI budget still constrained runtime", steps)
	}
}

type deliveryToolNative struct {
	*fakeTools
	status  linuxnative.ToolStatus
	retries int
}

func (n *deliveryToolNative) ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error) {
	return n.status, nil
}
func (n *deliveryToolNative) RetryMediaJob(context.Context, string) (linuxnative.ToolStatus, error) {
	n.retries++
	return n.status, nil
}
func TestDeliveryToolsKeepConversationNativeAndMediaPermissionBoundaries(t *testing.T) {
	f, p := trustedAgent(t)
	n := &deliveryToolNative{fakeTools: &fakeTools{fakeNative: f.native}, status: linuxnative.ToolStatus{ID: "original-job", ThreadID: "wxid_owner", SourceID: "original-source", State: "needs_review"}}
	f.manager.native = n
	args := json.RawMessage(`{"job_id":"original-job","request_key":"resume-once"}`)
	status, err := p.Call(context.Background(), "wechat_delivery_status", json.RawMessage(`{"job_id":"original-job"}`))
	if err != nil || !strings.Contains(status, `"retryable":false`) {
		t.Fatal("explicit native no-retry decision missing", status, err)
	}
	if _, err := p.Call(context.Background(), "wechat_delivery_retry", args); err == nil || n.retries != 0 {
		t.Fatal("native no-retry guard bypassed")
	} else if message, code := safeToolFailure(err); code != "agent_local_operation_failed" || !strings.Contains(message, "原媒体来源") {
		t.Fatal("native refusal mislabeled as connectivity failure", message, code)
	}
	n.status.Retryable = true
	if err := f.manager.tools.upload(stagedUpload{UploadSource: linuxnative.UploadSource{ID: "original-source", Filename: "voice.wav", ByteLength: 10}, Account: "test", Kind: "voice"}); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVoice = false
	f.manager.mu.Unlock()
	if _, err := p.Call(context.Background(), "wechat_delivery_retry", args); err == nil || n.retries != 0 {
		t.Fatal("media permission bypassed")
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVoice = true
	f.manager.mu.Unlock()
	for i := 0; i < 2; i++ {
		if _, err := p.Call(context.Background(), "wechat_delivery_retry", args); err != nil {
			t.Fatal(err)
		}
	}
	if n.retries != 1 {
		t.Fatal("native resume repeated", n.retries)
	}
	n.status.ThreadID = "other@chatroom"
	if _, err := p.Call(context.Background(), "wechat_delivery_status", json.RawMessage(`{"job_id":"original-job"}`)); err == nil {
		t.Fatal("cross-thread receipt exposed")
	}
	n.status.ThreadID = "wxid_owner"
	p.trace.Sender = "imposter"
	if _, err := p.Call(context.Background(), "wechat_delivery_retry", args); err == nil {
		t.Fatal("non-owner resumed job")
	}
}

func TestOwnerActionContinuationDoesNotRequireProactiveGreeting(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Proactive = false
	f.manager.config.Companion.ProactiveThreads = nil
	f.manager.mu.Unlock()
	_, err := p.Call(context.Background(), "agent_task", json.RawMessage(`{"key":"repair","title":"检查服务","checkpoint":"继续验证服务并反馈结果","state":"waiting","delay_seconds":30}`))
	if err != nil {
		t.Fatal(err)
	}
	task := f.manager.agent.list("test", "wxid_owner")[0]
	task.Runs = 40 // Legacy six-run budget must not stop an owner request.
	task.Next = time.Now().Add(-time.Second)
	if err = f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	if !task.OwnerRequested {
		t.Fatal("trusted action was downgraded to proactive greeting")
	}
	in, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
	if err != nil || in == nil || in.Mode != "direct" {
		t.Fatal("owner continuation blocked by greeting policy", in, err)
	}
	task.State = "waiting"
	task.Next = time.Now().Add(-time.Second)
	if err = f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	c := f.manager.agent.config()
	c.OwnerIDs = nil
	c.PrivilegedThreads = nil
	c.Host.Mode = "disabled"
	if err = f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	if in, err = f.manager.claimAgentTask(time.Now(), func(string) bool { return false }); err != nil || in != nil {
		t.Fatal("revoked owner continued", in, err)
	}
}

func TestPartialDeliveryCreatesOneOwnerRecoveryCheckpoint(t *testing.T) {
	f, p := trustedAgent(t)
	task := agentTask{ID: "song-original", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", MusicJob: "generation-one", MusicPrepared: true, State: "waiting_delivery"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in := p.trace
	in.TaskID = task.ID
	d := bot.TaskDelivery{Trace: "delivery-trace", State: "needs_review", Expected: 1, AcceptedParts: 2, TotalParts: 3, PendingJob: "send-pending", FailureReason: "transport_unavailable"}
	for i := 0; i < 2; i++ {
		if err := f.manager.updateAgentDelivery(in, d); err != nil {
			t.Fatal(err)
		}
	}
	tasks := f.manager.agent.list("test", "wxid_owner")
	if len(tasks) != 2 {
		t.Fatal("duplicate or absent recovery", tasks)
	}
	var recovery agentTask
	for _, v := range tasks {
		if v.ID != task.ID {
			recovery = v
		}
	}
	if !recovery.OwnerRequested || recovery.MusicJob != "" || recovery.State != "waiting" {
		t.Fatal("recovery would regenerate or require greeting", recovery)
	}
}

func TestGroupDeliveryReportsGoToOwnerPrivateWithoutMemberEscalation(t *testing.T) {
	for _, sender := range []string{"wxid_owner", "wxid_member"} {
		t.Run(sender, func(t *testing.T) {
			f, p := trustedAgent(t)
			f.manager.mu.Lock()
			f.manager.config.Companion.Enabled = true
			f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "source@chatroom", Name: "来源群", Mode: "mention"})
			f.manager.mu.Unlock()
			ac := f.manager.agent.config()
			ac.PrivilegedThreads = []string{"*"}
			if err := f.manager.agent.save(ac); err != nil {
				t.Fatal(err)
			}
			original := agentTask{ID: "source-song", Account: "test", Thread: "source@chatroom", Sender: sender, MusicJob: "generation", State: "waiting_delivery"}
			if err := f.manager.agent.task(original); err != nil {
				t.Fatal(err)
			}
			in := companion.Input{Account: "test", Thread: original.Thread, Sender: sender, TaskID: original.ID}
			if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: "delivery", State: "needs_review", Expected: 1, PendingJob: "send-original", AcceptedParts: 2, TotalParts: 3}); err != nil {
				t.Fatal(err)
			}
			private := f.manager.agent.list("test", "wxid_owner")
			if len(private) != 1 || private[0].ReportSourceThread != original.Thread || private[0].QuoteServerID != "" {
				t.Fatal("report leaked or lost source", private)
			}
			report := private[0]
			report.Next = time.Now().Add(-time.Second)
			if err := f.manager.agent.task(report); err != nil {
				t.Fatal(err)
			}
			claimed, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
			if err != nil || claimed == nil || claimed.Thread != "wxid_owner" || claimed.IsGroup {
				t.Fatal("report routed to group", claimed, err)
			}
			if sender == "wxid_member" {
				if !claimed.ReportOnly {
					t.Fatal("member-triggered failure elevated to owner")
				}
				p.SetTrace(*claimed)
				tools, err := p.List(context.Background())
				if err != nil || len(tools) != 0 || p.privileged() {
					t.Fatal("report-only context exposed tools", tools, err)
				}
				if _, err := p.Call(context.Background(), "server_exec", json.RawMessage(`{"path":"/","command":"true"}`)); err == nil {
					t.Fatal("report-only executed a program")
				}
			}
		})
	}
}

func TestMusicServiceHealthIsReadOnlyAndNeverSubmitsGeneration(t *testing.T) {
	requests := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/health" {
			t.Error("health attempted an effect", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ready":true,"bridge_version":7}`))
	}))
	defer backend.Close()
	f := setup(t)
	c := f.manager.speech
	c.YuEEnabled = true
	c.YuEEndpoint = backend.URL
	c.YuEToken = "synthetic-token"
	f.manager.speech = c
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	defer p.hub.Close()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic", TraceID: "health"})
	result, err := p.Call(context.Background(), "music_service_status", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var health yueHealth
	if json.Unmarshal([]byte(result), &health) != nil || !health.Ready || requests != 1 {
		t.Fatal(result, requests)
	}
	if len(f.manager.agent.list("test", "")) != 0 || len(f.manager.agent.operations()) != 0 {
		t.Fatal("health created a task or effect")
	}
	p.trace.Preview = true
	if _, err = p.Call(context.Background(), "music_service_status", json.RawMessage(`{}`)); err == nil {
		t.Fatal("preview called native health")
	}
}
