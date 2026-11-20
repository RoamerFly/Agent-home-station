package station

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

func TestLiveStationExternalTasksThroughChat(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("AHS_LOCAL_EXTERNAL_TASK_TEST") != "1" {
		t.Skip("set AHS_LOCAL_EXTERNAL_TASK_TEST=1 for installed Windows runtime acceptance")
	}
	// Run the compiled test beside the portable env directory so that the
	// restarted host can also resolve its cold-launch runtime during setup.
	options := localProjectToolWindowsOptionsFor(t, localExternalPackage)
	f := localChatFixture(t)
	f.handler.chat.projectTools.options = options
	route := "/api/local/project-tools/" + localExternalPackage
	if code, out := f.request("PUT", route, map[string]any{"action": "install", "approve_artifacts": true}, true); code != 200 {
		t.Fatal("install", code, out)
	}
	if code, out := f.request("PUT", route, map[string]any{"action": "enable", "revision": localProjectToolRevision(t, localProjectToolByID(t, f, localExternalPackage))}, true); code != 200 {
		t.Fatal("enable", code, out)
	}

	const secret = "synthetic-async-test-secret"
	binary := []byte("external Poll immediate binary\n")
	var jobStarts, jobPolls, jobCancels, pollStarts, pollQueries, binaryCalls, unknownStarts atomic.Int32
	reply := func(w http.ResponseWriter, value map[string]string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("declared credential did not reach authorized service")
			http.Error(w, "missing credential", 401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/job":
			jobStarts.Add(1)
			var args map[string]string
			if json.NewDecoder(r.Body).Decode(&args) != nil || (args["variant"] != "running" && args["variant"] != "cancel") {
				t.Error("missing Job body")
				http.Error(w, "wrong body", 400)
				return
			}
			reply(w, map[string]string{"provider_id": "private-job-" + args["variant"], "state": "pending"})
		case r.Method == "GET" && r.URL.Path == "/job/private-job-running":
			jobPolls.Add(1)
			reply(w, map[string]string{"state": "done", "result": "verified-job-result-123 " + secret})
		case r.Method == "POST" && r.URL.Path == "/cancel/private-job-cancel":
			jobCancels.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && r.URL.Path == "/poll":
			pollStarts.Add(1)
			reply(w, map[string]string{"lookup": "private-poll-seed", "state": "pending"})
		case r.Method == "GET" && r.URL.Path == "/poll/private-poll-seed":
			if pollQueries.Add(1) == 1 {
				reply(w, map[string]string{"lookup": "changed-untrusted-token", "state": "pending"})
			} else {
				reply(w, map[string]string{"state": "done", "result": "verified-poll-result-456"})
			}
		case r.Method == "POST" && r.URL.Path == "/binary":
			binaryCalls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binary)
		case r.Method == "POST" && r.URL.Path == "/unknown":
			unknownStarts.Add(1)
			reply(w, map[string]string{"state": "pending"})
		default:
			t.Error("unexpected external operation", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	schema := json.RawMessage(`{"type":"object","properties":{"variant":{"type":"string"}},"additionalProperties":false}`)
	tool := func(name, route, poll string) plugins.APITool {
		return plugins.APITool{Name: name, Description: "Exercise real external task transport", Method: "POST", URL: server.URL + route, InputSchema: schema,
			Headers: map[string]string{"Authorization": "Bearer {{secret.key}}"},
			Poll:    &plugins.PollSpec{URL: server.URL + poll, StatusPath: "state", DoneValues: []string{"done"}, FailedValues: []string{"failed"}, TextPath: "result", IntervalSeconds: 2, MaxAttempts: 3}}
	}
	job := tool("job", "/job", "/job/{{result.provider_id}}")
	job.Job = &plugins.JobSpec{IDPath: "provider_id", MaxPolls: 3, DeadlineSeconds: 30, Cancel: &plugins.CancelSpec{URL: server.URL + "/cancel/{{result.provider_id}}"}}
	job.Poll.IntervalSeconds = 4
	poll := tool("poll", "/poll", "/poll/{{result.lookup}}")
	media := tool("binary", "/binary", "/poll/{{result.lookup}}")
	media.MediaKind = "file"
	unknown := job
	unknown.Name, unknown.URL, unknown.Job = "unknown", server.URL+"/unknown", &plugins.JobSpec{IDPath: "provider_id"}
	job.BodyTemplate = json.RawMessage(`{"variant":"{{arg.variant}}"}`)
	manifest := plugins.Manifest{ID: "async", Name: "External async fixtures", Version: "1.0.0", Tools: []plugins.APITool{job, poll, media, unknown}}
	config := map[string]any{"connections": []plugins.Installed{{Manifest: manifest, Enabled: true, Secrets: map[string]string{"key": secret}}}}
	registration := stationToolRegistrationPath(f.handler.home.dataDir, localExternalRegistration)
	raw, _ := json.Marshal(config)
	if os.MkdirAll(filepath.Dir(registration), 0700) != nil || os.WriteFile(registration, raw, 0600) != nil {
		t.Fatal("prepare isolated registration")
	}
	backend := localProjectToolByID(t, f, localExternalPackage)
	if reason, _ := backend["reason"].(string); strings.Contains(reason, "私有登记目录") {
		t.Fatal("async registration not recognized", reason)
	}

	taskID := regexp.MustCompile(`"task_id":"([a-f0-9]{64})"`)
	extract := func(history string) string {
		match := taskID.FindAllStringSubmatch(strings.ReplaceAll(history, "\\", ""), -1)
		if len(match) == 0 {
			t.Error("task id unavailable in actual result")
			return ""
		}
		return match[len(match)-1][1]
	}
	assertPublic := func(text string) {
		for _, value := range []string{secret, server.URL, "private-job-", "private-poll-seed", "changed-untrusted-token"} {
			if strings.Contains(text, value) {
				t.Error("private provider material reached the model")
			}
		}
	}
	const response = `{"action":"reply","messages":["已核对。"]}`
	do := func(id string, actions ...func(string, string) string) {
		t.Helper()
		step := 0
		f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
			return localModelFunc(func(_ context.Context, system string, turns []map[string]string) (string, error) {
				history := localModelHistory(turns)
				assertPublic(system + history)
				if step >= len(actions) {
					return response, nil
				}
				action := actions[step]
				step++
				return action(system, history), nil
			}), nil
		}
		if code, out := localChatSend(f, id, "验收 "+id); code != 200 || out["status"] != "completed" || step != len(actions) {
			t.Fatalf("%s: %d step %d, %v", id, code, step, out)
		}
	}
	start := func(name string, arguments map[string]any) string {
		return localToolCall(localExternalTaskCall, map[string]any{"action": "start", "tool": name, "arguments": arguments})
	}
	status := func(action, id string) string {
		return localToolCall(localExternalTaskCall, map[string]any{"action": action, "task_id": id})
	}
	expect := func(text string, values ...string) {
		text = strings.ReplaceAll(text, "\\", "")
		for _, value := range values {
			if !strings.Contains(text, value) {
				t.Errorf("missing %q in model context", value)
			}
		}
	}

	var scheduled, followupVerified atomic.Bool
	var startRounds atomic.Int32
	var jobID string
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, turns []map[string]string) (string, error) {
			history := localModelHistory(turns)
			assertPublic(system + history)
			switch startRounds.Add(1) {
			case 1:
				expect(system, `"name":"`+localExternalTaskCall+`"`)
				return localToolCall(localExternalTaskList, map[string]any{"query": "async", "limit": 2}), nil
			case 2:
				expect(history, "async__binary", "async__job", `"has_more":true`)
				return start("async__job", map[string]any{"variant": "running"}), nil
			case 3:
				expect(history, `"state":"waiting"`)
				jobID = extract(history)
				return localToolCall(localFollowupToolName, map[string]string{"action": "schedule", "job_id": jobID, "result_tool": localExternalTaskCall}), nil
			case 4:
				expect(history, "local-task-")
				scheduled.Store(true)
				return response, nil
			default:
				t.Error("background result advanced before restart")
				return response, nil
			}
		}), nil
	}
	if code, out := localChatSend(f, "external-task-create", "开始外部任务，完成后报告。"); code != 200 || out["status"] != "completed" || !scheduled.Load() {
		t.Fatal("schedule external Job", code, out)
	}
	f = localRestartChat(t, f)
	f.handler.chat.projectTools.options = options
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, turns []map[string]string) (string, error) {
			text := localModelHistory(turns)
			assertPublic(system + text)
			expect(text, "verified-job-result-123", "[已隐藏]", `"background_result":true`)
			followupVerified.Store(true)
			return response, nil
		}), nil
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, page := f.request("GET", "/api/local/chat", nil, false)
		if code == 200 && page["background_pending"] == false && followupVerified.Load() {
			break
		}
		if time.Now().After(deadline) {
			_, queue := f.request("GET", "/api/local/task-followups", nil, false)
			t.Fatalf("followup pending after restart (published=%v, polls=%d, status=%d, queue=%v)", followupVerified.Load(), jobPolls.Load(), code, queue)
		}
		time.Sleep(100 * time.Millisecond)
	}
	do("read-job-page", func(_, _ string) string {
		return localToolCall(localExternalTaskCall, map[string]any{"action": "status", "task_id": jobID, "text_offset": 9, "text_limit": 3})
	}, func(_, history string) string {
		expect(history, `"text":"job"`, `"next_text_offset":12`)
		return response
	})

	var cancelID, pollID, artifactID string
	do("external-cancel-start", func(_, _ string) string {
		return start("async__job", map[string]any{"variant": "cancel"})
	}, func(_, history string) string { cancelID = extract(history); return response })
	do("external-cancel-phase", func(_, _ string) string { return status("cancel", cancelID) },
		func(_, history string) string {
			expect(history, `"state":"cancel_acknowledged"`, `"cancel_scope":"remote_request"`)
			return status("cancel", cancelID)
		},
		func(_, history string) string { expect(history, `"state":"cancel_acknowledged"`); return response })

	do("external-poll-start", func(_, _ string) string { return start("async__poll", map[string]any{}) },
		func(_, history string) string {
			pollID = extract(history)
			expect(history, `"state":"waiting"`)
			return status("status", pollID)
		},
		func(_, history string) string { expect(history, `"attempts":0`); return response })
	time.Sleep(2100 * time.Millisecond)
	do("external-poll-next", func(_, _ string) string { return status("status", pollID) },
		func(_, history string) string { expect(history, `"state":"waiting"`, `"attempts":1`); return response })
	f = localRestartChat(t, f)
	f.handler.chat.projectTools.options = options
	time.Sleep(2100 * time.Millisecond)
	do("external-poll-done", func(_, _ string) string { return status("status", pollID) },
		func(_, history string) string {
			expect(history, "verified-poll-result-456", `"state":"completed"`)
			return response
		})

	do("external-poll-stop", func(_, _ string) string { return start("async__poll", map[string]any{}) },
		func(_, history string) string { return status("cancel", extract(history)) },
		func(_, history string) string {
			expect(history, `"state":"cancelled"`, `"cancel_scope":"local_polling"`)
			return status("status", extract(history))
		},
		func(_, history string) string { expect(history, `"state":"cancelled"`); return response })
	do("external-poll-binary", func(_, _ string) string { return start("async__binary", map[string]any{}) },
		func(_, history string) string {
			ids := localPublicationWorkIDs(history)
			if len(ids) != 1 {
				t.Error("missing binary artifact", history)
			} else {
				artifactID = ids[0]
			}
			return response
		})
	if artifactID != "" && !bytes.Equal(localArtifactDownload(t, f, artifactID), binary) {
		t.Fatal("verified artifact did not match actual bytes")
	}
	do("external-unknown-start", func(_, _ string) string { return start("async__unknown", map[string]any{}) },
		func(_, history string) string {
			expect(history, `"state":"needs_review"`)
			return start("async__unknown", map[string]any{})
		},
		func(_, history string) string { expect(history, `"state":"needs_review"`); return response })
	if err := os.Remove(registration); err != nil {
		t.Fatal(err)
	}
	do("external-task-revoked", func(system, _ string) string {
		if strings.Contains(system, `"name":"`+localExternalTaskCall+`"`) {
			t.Error("revoked async registration still discoverable")
		}
		return response
	})
	if jobStarts.Load() != 2 || jobPolls.Load() != 1 || jobCancels.Load() != 1 || pollStarts.Load() != 2 || pollQueries.Load() != 2 || binaryCalls.Load() != 1 || unknownStarts.Load() != 1 {
		t.Fatalf("unexpected provider counts: job=%d/%d/%d poll=%d/%d binary=%d unknown=%d",
			jobStarts.Load(), jobPolls.Load(), jobCancels.Load(), pollStarts.Load(), pollQueries.Load(), binaryCalls.Load(), unknownStarts.Load())
	}
	t.Log("real HTTP starts=6 job_status=1 remote_cancel=1 poll_queries=2 local_stop=true binary_verified=true background_restart=true unknown_no_replay=true revoked_hidden=true")
}
