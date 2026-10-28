package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestRVCResumeKeepsOriginalScopedTaskAndNeverStagesMedia(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "resume-test"})
	state, posts, reads := "needs_review", 0, 0
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jobs/synthetic-rvc":
			reads++
			json.NewEncoder(w).Encode(map[string]any{"id": "synthetic-rvc", "engine": "rvc", "state": state})
		case "/jobs/synthetic-rvc/resume":
			if r.Method != "POST" {
				t.Error("resume method", r.Method)
			}
			posts++
			state = "queued"
			w.Write([]byte(`{"id":"synthetic-rvc","engine":"rvc","state":"queued"}`))
		default:
			t.Error("resume created/downloaded/staged another song", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer u.Close()
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, u.URL, "synthetic-token"
	task := agentTask{ID: "original-task", Account: "test", Thread: "filehelper", Sender: "owner", MusicJob: "synthetic-rvc", MusicSpec: &songSpec{Engine: "rvc"}, RecoveryMessageID: "original-request", MusicFailure: true, MusicPrepared: true, State: "needs_review", OriginTrace: "original-trace", MusicKind: "voice"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"job_id":"synthetic-rvc"}`)
	for i := 0; i < 2; i++ {
		result, err := p.Call(context.Background(), "music_local_resume", input)
		if err != nil || !strings.Contains(result, `"automatic_followup":true`) || !strings.Contains(result, `"job_id":"synthetic-rvc"`) {
			t.Fatal(result, err)
		}
	}
	if posts != 1 || reads != 2 {
		t.Fatal("compute resubmitted", posts, reads)
	}
	tasks := f.manager.agent.list("test", "filehelper")
	if len(tasks) != 1 || tasks[0].State != "waiting" || tasks[0].MusicFailure || tasks[0].MusicPrepared || tasks[0].OriginTrace != "original-trace" || !tasks[0].Next.After(time.Now()) {
		t.Fatal("original continuation lost", tasks)
	}
	p.trace.Thread = "other-room"
	if _, err := p.Call(context.Background(), "music_local_resume", input); err == nil || posts != 1 || reads != 2 {
		t.Fatal("cross-conversation compute accessed", err, posts, reads)
	}
	p.trace.Thread, p.trace.Account = "filehelper", "another-account"
	if _, err := p.Call(context.Background(), "music_local_resume", input); err == nil || reads != 2 {
		t.Fatal("cross-account compute accessed", err)
	}
}

func TestRVCResumeDoesNotReviveCancelledDeliveredTasksOrTrustWrongReply(t *testing.T) {
	for _, test := range []struct {
		name, state, engine, reply, taskState string
		wantPost, wantErr                     bool
	}{
		{"cancelled", "cancelled", "rvc", "", "cancelled", false, true},
		{"unknown-engine", "needs_review", "unknown", "", "needs_review", false, true},
		{"already-complete", "completed", "rvc", "", "completed", false, false},
		{"delivered-task", "running", "rvc", "", "completed", false, false},
		{"cancelled-task", "running", "rvc", "", "cancelled", false, false},
		{"cancelled-task-failed-compute", "failed", "rvc", "", "cancelled", false, true},
		{"wrong-reply", "needs_review", "rvc", `{"id":"wrong","engine":"rvc","state":"queued"}`, "needs_review", true, true},
		{"missing-engine", "needs_review", "rvc", `{"id":"job","state":"queued"}`, "needs_review", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			p, _, _ := f.manager.toolAccess()
			p.SetTrace(companion.Input{Account: "test", Thread: "filehelper", Sender: "owner"})
			posts := 0
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
					w.Write([]byte(test.reply))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "job", "state": test.state, "engine": test.engine})
			}))
			defer u.Close()
			f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, u.URL, "synthetic-token"
			task := agentTask{ID: "original", Account: "test", Thread: "filehelper", Sender: "owner", MusicJob: "job", MusicSpec: &songSpec{Engine: "rvc"}, RecoveryMessageID: "request", State: test.taskState}
			if err := f.manager.agent.task(task); err != nil {
				t.Fatal(err)
			}
			result, err := p.callYuE(context.Background(), "music_local_resume", json.RawMessage(`{"job_id":"job"}`))
			if (err != nil) != test.wantErr || (posts == 1) != test.wantPost {
				t.Fatal(result, err, posts)
			}
			if got := f.manager.agent.list("test", "filehelper"); len(got) != 1 || got[0].State != test.taskState {
				t.Fatal(got)
			}
			if strings.Contains(result, `"automatic_followup":true`) {
				t.Fatal("retired task revived", result)
			}
		})
	}
}

func TestYuEResumePassesExplicitRestartAndRetainsConversationScope(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "reattach", true: "restart-missing"}[restart], func(t *testing.T) {
			f := setup(t)
			p, _, _ := f.manager.toolAccess()
			p.SetTrace(companion.Input{Account: "test", Thread: "filehelper", Sender: "owner"})
			posts := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/jobs/yue-original" && r.URL.Path != "/jobs/yue-original/resume" {
					t.Error("unexpected generation or audio request", r.URL.Path)
				}
				state := "needs_review"
				if r.Method == "POST" {
					posts++
					var options map[string]any
					if json.NewDecoder(r.Body).Decode(&options) != nil || len(options) != 1 || options["restart_missing"] != restart {
						t.Error("restart choice changed", options)
					}
					state = "queued"
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "yue-original", "engine": "yue", "state": state})
			}))
			defer upstream.Close()
			f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, upstream.URL, "synthetic-token"
			task := agentTask{ID: "original", Account: "test", Thread: "filehelper", Sender: "owner", MusicJob: "yue-original", MusicSpec: &songSpec{}, RecoveryMessageID: "request", State: "needs_review", OriginTrace: "original-trace"}
			if err := f.manager.agent.task(task); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"job_id": "yue-original", "restart_missing": restart})
			result, err := p.Call(context.Background(), "music_local_resume", raw)
			if err != nil || posts != 1 || !strings.Contains(result, `"automatic_followup":true`) {
				t.Fatal(result, err, posts)
			}
			if tasks := f.manager.agent.list("test", "filehelper"); len(tasks) != 1 || tasks[0].MusicJob != "yue-original" || tasks[0].State != "waiting" || tasks[0].OriginTrace != "original-trace" {
				t.Fatal("lost original task", tasks)
			}
			p.trace.Thread = "other-group"
			if _, err := p.Call(context.Background(), "music_local_resume", raw); err == nil || posts != 1 {
				t.Fatal("cross-session generation recovered", err, posts)
			}
		})
	}
}

func TestYuERecoveryFailureExposesActionableOriginalTaskReason(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"yue_recovery_prompt_missing_explicit_restart_required"}`))
	}))
	defer upstream.Close()
	f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = upstream.URL, "synthetic-token"
	_, _, err := p.yueRequest(context.Background(), "POST", "/jobs/original/resume", map[string]any{}, "")
	if err == nil || !strings.Contains(err.Error(), "restart_missing=true") || !strings.Contains(err.Error(), "同一个job_id") {
		t.Fatal("specific recovery choice hidden", err)
	}
}
