package admin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOwnerRequestIsAuthenticatedIdempotentAndUsesProductionInput(t *testing.T) {
	f, _ := trustedAgent(t)
	f.manager.config.Companion.Enabled = true
	in := map[string]string{"recipient": "wxid_owner", "text": "一分钟后私聊发你好", "request_key": "timer-1"}
	if code, _, _ := f.request("POST", "/api/agent/owner-requests", in, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/agent/owner-requests", in, false, false); code != 403 {
		t.Fatal("missing CSRF", code)
	}
	if code, _, _ := f.request("POST", "/api/agent/owner-requests", map[string]string{"recipient": "wxid_member", "text": "x", "request_key": "bad"}, true, true); code != 403 {
		t.Fatal("untrusted owner accepted", code)
	}
	for _, want := range []int{202, 200} {
		code, b, _ := f.request("POST", "/api/agent/owner-requests", in, true, true)
		if code != want || !strings.Contains(string(b), "synthetic_authenticated_admin_request") {
			t.Fatal(code, string(b))
		}
	}
	in["text"] = "different"
	if code, _, _ := f.request("POST", "/api/agent/owner-requests", in, true, true); code != 409 {
		t.Fatal("changed idempotency key admitted", code)
	}
	tasks := f.manager.agent.list("test", "wxid_owner")
	if len(tasks) != 1 || !tasks[0].OwnerRequested || !tasks[0].OwnerRequest {
		t.Fatal(tasks)
	}
	claim, e := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
	if e != nil || claim == nil || !strings.Contains(claim.Text, "非真实微信入站") || !strings.Contains(claim.Text, "本条新请求接收时间") || strings.Contains(claim.Text, "0001-01-01") || !strings.Contains(claim.Text, "一分钟后私聊发你好") {
		t.Fatal(claim, e)
	}
}

func TestTaskHistoryAndScheduleHaveNoLegacyQuota(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.MaxSteps = 0
	c.MaxTaskRuns = 0
	if e := f.manager.agent.save(c); e != nil {
		t.Fatal("legacy ignored fields became quotas", e)
	}
	if _, e := p.Call(t.Context(), "agent_task", json.RawMessage(`{"key":"later","title":"三天后","checkpoint":"检查进展","state":"waiting","delay_seconds":259200}`)); e != nil {
		t.Fatal("one-day scheduling quota", e)
	}
	f.manager.agent.mu.Lock()
	f.manager.agent.data.Operations["historical"] = agentOperation{ID: "historical", Tool: "server_exec", Result: strings.Repeat("x", (8<<20)+1)}
	e := f.manager.agent.persist()
	f.manager.agent.mu.Unlock()
	if e != nil {
		t.Fatal("history byte quota blocked persistence", e)
	}
	if _, e := newAgentControl(f.dir); e != nil {
		t.Fatal("history byte quota blocked restart", e)
	}
}

func TestClockAndTaskFileExposeActualDueTime(t *testing.T) {
	f, p := trustedAgent(t)
	result, e := p.Call(t.Context(), "agent_clock", json.RawMessage(`{}`))
	var clock struct {
		UTC  string `json:"utc"`
		Unix int64  `json:"unix_seconds"`
	}
	if e != nil || json.Unmarshal([]byte(result), &clock) != nil || clock.UTC == "" || clock.Unix < time.Now().Unix()-2 {
		t.Fatal(result, e)
	}
	result, e = p.Call(t.Context(), "agent_task", json.RawMessage(`{"key":"timer","title":"测试定时任务","checkpoint":"到点私聊发你好","state":"waiting","delay_seconds":60}`))
	if e != nil {
		t.Fatal(result, e)
	}
	before := f.manager.agent.list("test", "wxid_owner")[0]
	if before.Next.Before(time.Now().Add(55*time.Second)) || !before.OwnerRequested {
		t.Fatal(before)
	}
	loaded, e := newAgentControl(f.dir)
	if e != nil {
		t.Fatal(e)
	}
	after := loaded.list("test", "wxid_owner")[0]
	if !after.Next.Equal(before.Next) || after.Checkpoint != before.Checkpoint {
		t.Fatal("scheduled task lost on disk", after)
	}
}

func TestOwnerCanSendToCurrentPrivateThreadWithoutReplay(t *testing.T) {
	f, p := trustedAgent(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	c := f.manager.agent.config()
	c.CrossConversation = true
	if e := f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	f.manager.config.Behavior.RandomDelay = false
	args := json.RawMessage(`{"thread_id":"wxid_owner","text":"你好","request_key":"scheduled-greeting"}`)
	for i := 0; i < 2; i++ {
		if result, e := p.Call(t.Context(), "wechat_send_text", args); e != nil || !strings.Contains(result, "operation_id") {
			t.Fatal(result, e)
		}
	}
	if native.submits.Load() != 1 {
		t.Fatal("same logical send was replayed", native.submits.Load())
	}
	_, e := p.Call(t.Context(), "agent_task", json.RawMessage(`{"id":"invented","checkpoint":"x","state":"completed"}`))
	var safe interface{ SafeToolFailure() (string, string) }
	if !errors.As(e, &safe) {
		t.Fatal("local task validation hid actionable error", e)
	}
}
