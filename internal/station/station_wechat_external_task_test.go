package station

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/workspacefs"
)

func TestWechatExternalJobAndPollUseDurableApplicationTasks(t *testing.T) {
	if os.Getenv("AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT") == "" {
		t.Skip("Windows project-tool runtime is required")
	}
	f := localChatFixture(t)
	f.handler.chat.projectTools.options = localProjectToolWindowsOptionsFor(t, localExternalPackage)
	if code, out := f.request("GET", "/api/local/project-tools", nil, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	route := "/api/local/project-tools/" + localExternalPackage
	for _, action := range []string{"install", "enable"} {
		input := map[string]any{"action": action}
		if action == "install" {
			input["approve_artifacts"] = true
		} else {
			input["revision"] = localProjectToolRevision(t, localProjectToolByID(t, f, localExternalPackage))
		}
		if code, out := f.request("PUT", route, input, true); code != http.StatusOK {
			t.Fatal(code, out)
		}
	}
	var jobStarts, jobPolls, jobCancels, pollStarts, pollQueries, binaryStarts, unknownStarts, mediaStarts, mediaFetches atomic.Int32
	const secret = "synthetic-task-secret"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "ticket=private" {
				t.Error("media request forwarded a credential or lost its signed URL")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mediaFetches.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("verified-url-media"))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/job":
			jobStarts.Add(1)
			_, _ = w.Write([]byte(`{"id":"private-job-123","state":"pending"}`))
		case "/job/private-job-123":
			jobPolls.Add(1)
			_, _ = w.Write([]byte(`{"state":"done","result":"verified-job-` + secret + `"}`))
		case "/cancel/private-job-123":
			jobCancels.Add(1)
			_, _ = w.Write([]byte(`{"accepted":true}`))
		case "/poll":
			pollStarts.Add(1)
			_, _ = w.Write([]byte(`{"lookup":"private-poll-456","state":"pending"}`))
		case "/poll/private-poll-456":
			pollQueries.Add(1)
			_, _ = w.Write([]byte(`{"state":"done","result":"verified-poll-result"}`))
		case "/binary":
			binaryStarts.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("verified-immediate-binary"))
		case "/unknown":
			unknownStarts.Add(1)
			_, _ = w.Write([]byte(`{"state":"pending"}`))
		case "/media-job":
			mediaStarts.Add(1)
			_, _ = w.Write([]byte(`{"id":"private-media-789","state":"done","asset_url":"` + server.URL + `/asset?ticket=private"}`))
		case "/bad-media-job":
			_, _ = w.Write([]byte(`{"id":"private-media-bad","state":"done","asset_url":"http://127.0.0.1:1/unapproved"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	schema := json.RawMessage(`{"type":"object","properties":{"variant":{"type":"string"}},"additionalProperties":false}`)
	job := plugins.APITool{Name: "job", URL: server.URL + "/job", Method: "POST", InputSchema: schema,
		Headers: map[string]string{"Authorization": "Bearer {{secret.key}}"},
		Job: &plugins.JobSpec{IDPath: "id", DeadlineSeconds: 30, MaxPolls: 3,
			Cancel: &plugins.CancelSpec{URL: server.URL + "/cancel/{{result.id}}"}},
		Poll: &plugins.PollSpec{URL: server.URL + "/job/{{result.id}}", StatusPath: "state", DoneValues: []string{"done"},
			TextPath: "result", IntervalSeconds: 1}}
	poll := plugins.APITool{Name: "poll", URL: server.URL + "/poll", Method: "POST", InputSchema: schema,
		Headers: map[string]string{"Authorization": "Bearer {{secret.key}}"},
		Poll: &plugins.PollSpec{URL: server.URL + "/poll/{{result.lookup}}", StatusPath: "state", DoneValues: []string{"done"},
			TextPath: "result", IntervalSeconds: 1, MaxAttempts: 3}}
	binary := poll
	binary.Name, binary.URL, binary.MediaKind = "binary", server.URL+"/binary", "file"
	unknown := job
	unknown.Name, unknown.URL = "unknown", server.URL+"/unknown"
	media := job
	media.Name, media.URL, media.MediaKind = "media", server.URL+"/media-job", "file"
	media.Poll = &plugins.PollSpec{URL: server.URL + "/job/{{result.id}}", StatusPath: "state", DoneValues: []string{"done"},
		MediaURLPath: "asset_url", IntervalSeconds: 1}
	badMedia := media
	badMedia.Name, badMedia.URL = "bad_media", server.URL+"/bad-media-job"
	registration := map[string]any{"connections": []plugins.Installed{{Manifest: plugins.Manifest{ID: "async", Name: "Synthetic async", Version: "1",
		Tools: []plugins.APITool{job, poll, binary, unknown, media, badMedia}}, Enabled: true, Secrets: map[string]string{"key": secret}}}}
	path := stationToolRegistrationPath(f.handler.home.dataDir, localExternalRegistration)
	raw, _ := json.Marshal(registration)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "native-token")
	if err := os.WriteFile(key, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:1", TokenFile: key, Account: "synthetic-account"})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	now := time.Now().UTC()
	in := companion.Input{Channel: "wechat", Account: native.Account(), Thread: "test@chatroom", Sender: "owner", TraceID: "synthetic-task",
		Text: "执行任务", MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true}
	in.Source = &companion.SourceRef{InstanceID: memory.SourceInstanceID(), MessageID: in.TraceID, Account: in.Account,
		Thread: in.Thread, Sender: in.Sender, OccurredAt: in.MessageTime}
	config := companion.Defaults()
	config.Permissions.Remember = true
	if err := memory.Observe(in, config, now); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = memory.SourceForInput(in)
	grant := now.Add(-time.Minute)
	adapter := &stationAttachmentTools{native: native, store: memory, dir: t.TempDir(), projects: f.handler.chat.projectTools,
		grants: map[string]time.Time{in.Thread: grant}, replies: map[string]time.Time{in.Thread: grant},
		blocked: func() bool { return false }, externalMu: new(sync.Mutex),
		projectOwnerAllowed: func(current companion.Input) bool { return current.Sender == in.Sender }}
	adapter.SetTrace(in)
	listed, err := adapter.List(t.Context())
	if err != nil || !hasTool(listed, localExternalTaskCall) {
		t.Fatal("async application tool unavailable", err)
	}
	call := func(action, tool, id, variant string) (map[string]any, string) {
		t.Helper()
		input := map[string]any{"action": action}
		if action == "start" {
			input["tool"], input["arguments"] = tool, map[string]string{"variant": variant}
		} else {
			input["task_id"] = id
		}
		args, _ := json.Marshal(input)
		text, err := adapter.Call(t.Context(), localExternalTaskCall, args)
		if err != nil {
			t.Fatal(action, tool, err)
		}
		var view map[string]any
		if json.Unmarshal([]byte(text), &view) != nil || strings.Contains(text, secret) || strings.Contains(text, server.URL) ||
			strings.Contains(text, "private-job-123") || strings.Contains(text, "private-poll-456") {
			t.Fatal("task leaked provider state", text)
		}
		return view, text
	}
	first, _ := call("start", "async__job", "", "run")
	id, _ := first["task_id"].(string)
	if first["state"] != "waiting" || !validLocalRVCHex(id, 64) || jobStarts.Load() != 1 {
		t.Fatal("Job start failed", first, jobStarts.Load())
	}
	call("start", "async__job", "", "run")
	if jobStarts.Load() != 1 {
		t.Fatal("duplicate Job submission")
	}
	j, err := adapter.readExternalTaskJob(id)
	if err != nil {
		t.Fatal(err)
	}
	j.Updated = now.Add(-2 * time.Second)
	if err := adapter.writeExternalTaskJob(j, nil); err != nil {
		t.Fatal(err)
	}
	completed, text := call("status", "", id, "")
	if completed["state"] != "completed" || !strings.Contains(text, "verified-job-[已隐藏]") || jobPolls.Load() != 1 {
		t.Fatal("Job result unavailable", text, jobPolls.Load())
	}
	call("status", "", id, "")
	if jobPolls.Load() != 1 {
		t.Fatal("completed Job polled again")
	}
	second, _ := call("start", "async__poll", "", "run")
	pollID, _ := second["task_id"].(string)
	if second["state"] != "waiting" || pollStarts.Load() != 1 {
		t.Fatal("Poll start failed", second, pollStarts.Load())
	}
	p, err := adapter.readExternalTaskJob(pollID)
	if err != nil {
		t.Fatal(err)
	}
	p.Updated = now.Add(-2 * time.Second)
	if err := adapter.writeExternalTaskJob(p, nil); err != nil {
		t.Fatal(err)
	}
	pollDone, _ := call("status", "", pollID, "")
	if pollDone["state"] != "completed" || pollQueries.Load() != 1 {
		t.Fatal("Poll result unavailable", pollDone, pollQueries.Load())
	}
	third, _ := call("start", "async__job", "", "cancel")
	cancelID, _ := third["task_id"].(string)
	cancelled, _ := call("cancel", "", cancelID, "")
	if cancelled["state"] != "cancel_acknowledged" || jobCancels.Load() != 1 {
		t.Fatal("remote cancellation unavailable", cancelled, jobCancels.Load())
	}
	call("cancel", "", cancelID, "")
	if jobCancels.Load() != 1 {
		t.Fatal("remote cancellation repeated")
	}
	binaryDone, _ := call("start", "async__binary", "", "run")
	binaryID, _ := binaryDone["task_id"].(string)
	if binaryDone["state"] != "completed" || binaryStarts.Load() != 1 || !validLocalRVCHex(binaryID, 64) {
		t.Fatal("immediate binary Poll result unavailable", binaryDone, binaryStarts.Load())
	}
	root, err := adapter.externalTaskRoot(binaryID)
	if err != nil {
		t.Fatal(err)
	}
	file, err := workspacefs.OpenRegular(root, "output.bin")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	_ = file.Close()
	_ = root.Close()
	if err != nil || string(data) != "verified-immediate-binary" {
		t.Fatal("binary result bytes changed", err)
	}
	mediaPending, _ := call("start", "async__media", "", "run")
	mediaID, _ := mediaPending["task_id"].(string)
	if mediaPending["state"] != "media_pending" || mediaStarts.Load() != 1 || mediaFetches.Load() != 0 {
		t.Fatal("verified media URL was not held privately", mediaPending)
	}
	mediaReady, _ := call("status", "", mediaID, "")
	if mediaReady["state"] != "completed" || mediaFetches.Load() != 1 {
		t.Fatal("verified media URL was not collected", mediaReady, mediaFetches.Load())
	}
	mediaRoot, err := adapter.externalTaskRoot(mediaID)
	if err != nil {
		t.Fatal(err)
	}
	mediaFile, err := workspacefs.OpenRegular(mediaRoot, "output.bin")
	if err != nil {
		t.Fatal(err)
	}
	mediaData, err := io.ReadAll(mediaFile)
	_ = mediaFile.Close()
	_ = mediaRoot.Close()
	if err != nil || string(mediaData) != "verified-url-media" {
		t.Fatal("collected media bytes changed", err)
	}
	badPending, _ := call("start", "async__bad_media", "", "run")
	badID, _ := badPending["task_id"].(string)
	badStatus, _ := call("status", "", badID, "")
	if badStatus["state"] != "media_pending" || mediaFetches.Load() != 1 {
		t.Fatal("unapproved media origin was fetched", badStatus, mediaFetches.Load())
	}
	unknownResult, _ := call("start", "async__unknown", "", "run")
	if unknownResult["state"] != "needs_review" || unknownStarts.Load() != 1 {
		t.Fatal("unknown submission state was not retained", unknownResult, unknownStarts.Load())
	}
	call("start", "async__unknown", "", "run")
	if unknownStarts.Load() != 1 {
		t.Fatal("unknown submission was repeated")
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	again, _ := call("start", "async__job", "", "run")
	if again["state"] != "needs_review" || jobStarts.Load() != 2 {
		t.Fatal("changed registration resubmitted Job", again, jobStarts.Load())
	}
}
