package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/comfyui"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type syntheticLocalComfy struct {
	root      string
	fish      string
	prompt    comfyui.JobRef
	image     comfyui.Image
	video     comfyui.MP4
	state     comfyui.State
	submits   int
	starts    int
	fetches   int
	cancels   int
	stops     int
	lost      bool
	running   bool
	failFetch bool
}

func (s *syntheticLocalComfy) Info(_ context.Context, fish string) (localComfyDeployment, error) {
	if s.fish != "" && s.fish != fish {
		return localComfyDeployment{}, errors.New("wrong fixture scope")
	}
	return localComfyDeployment{Binding: "fixture-comfy-binding", WorkflowRoot: s.root, Running: s.running}, nil
}

func (s *syntheticLocalComfy) Use(_ context.Context, fish, binding string, start bool, fn func(localComfyClient) error) error {
	if fish != s.fish || binding != "fixture-comfy-binding" {
		return errors.New("wrong fixture scope")
	}
	if start && !s.running {
		s.starts++
		s.running = true
	}
	if !s.running {
		return errLocalComfyNotRunning
	}
	return fn(s)
}

func (s *syntheticLocalComfy) Check(context.Context) error { return nil }

func (s *syntheticLocalComfy) MarkSettled(context.Context, comfyui.JobRef) error { return nil }

func (s *syntheticLocalComfy) Submit(_ context.Context, p comfyui.Prepared) error {
	s.submits++
	s.prompt = p.Ref()
	s.state = comfyui.Running
	if s.lost {
		return comfyui.ErrSubmissionUnknown
	}
	return nil
}

func (s *syntheticLocalComfy) Status(_ context.Context, ref comfyui.JobRef) (comfyui.JobStatus, error) {
	if ref != s.prompt {
		return comfyui.JobStatus{}, errors.New("looked up a different prompt")
	}
	return comfyui.JobStatus{State: s.state, ImageCount: 2}, nil
}

func (s *syntheticLocalComfy) VideoStatus(_ context.Context, ref comfyui.JobRef) (comfyui.VideoStatus, error) {
	if ref != s.prompt || len(s.video.Data) == 0 {
		return comfyui.VideoStatus{}, errors.New("fixture video unavailable")
	}
	return comfyui.VideoStatus{State: s.state, VideoCount: 1}, nil
}

func (s *syntheticLocalComfy) FetchMP4(_ context.Context, ref comfyui.JobRef) (comfyui.MP4, error) {
	if ref != s.prompt || s.state != comfyui.Completed || len(s.video.Data) == 0 {
		return comfyui.MP4{}, errors.New("fixture video unavailable")
	}
	s.fetches++
	return s.video, nil
}

func (s *syntheticLocalComfy) Cancel(_ context.Context, ref comfyui.JobRef) (bool, error) {
	if ref != s.prompt {
		return false, errors.New("cancelled a different prompt")
	}
	s.cancels++
	s.state = comfyui.Cancelled
	return true, nil
}

func (s *syntheticLocalComfy) FetchPNG(_ context.Context, ref comfyui.JobRef, index int) (comfyui.Image, error) {
	if ref != s.prompt || s.state != comfyui.Completed || index == 1 && s.failFetch {
		return comfyui.Image{}, errors.New("fixture image unavailable")
	}
	s.fetches++
	return s.image, nil
}

func (s *syntheticLocalComfy) Stop(context.Context) error {
	s.stops++
	s.running = false
	return nil
}

func (s *syntheticLocalComfy) Close(ctx context.Context) error { return s.Stop(ctx) }

func setUpLocalComfyChat(t *testing.T, f *localHomeFixture, backend localComfyBackend) {
	t.Helper()
	catalog, err := filepath.Abs(filepath.Join("..", "..", "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	f.handler.chat.projectTools.options.CatalogDirectory = catalog
	f.handler.chat.projectTools.comfy = backend
	const route = "/api/local/project-tools/comfyui"
	if code, _ := f.request("PUT", route, map[string]any{"action": "install"}, true); code != http.StatusBadRequest {
		t.Fatalf("ComfyUI installed without approval: %d", code)
	}
	if code, out := f.request("PUT", route, map[string]any{"action": "install", "approve_artifacts": true}, true); code != http.StatusOK {
		t.Fatalf("install ComfyUI: %d %#v", code, out)
	}
	installed := localProjectToolByID(t, f, localComfyPackage)
	if code, out := f.request("PUT", route, map[string]any{"action": "enable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatalf("enable ComfyUI: %d %#v", code, out)
	}
}

func TestWechatComfyWorkflowCatalogUsesWorkstationWithoutMessageSource(t *testing.T) {
	f := localChatFixture(t)
	record, err := f.handler.home.identity.Read()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeLocalComfyChatWorkflow(t, root)
	setUpLocalComfyChat(t, f, &syntheticLocalComfy{root: root, fish: record.FishID})
	key := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(key, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:1", TokenFile: key, Account: "synthetic-account"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	in := companion.Input{Channel: "wechat", Account: native.Account(), Thread: "test@chatroom", Sender: "owner",
		TraceID: "synthetic-message", MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true}
	adapter := &stationAttachmentTools{native: native, grants: map[string]time.Time{in.Thread: now.Add(-time.Minute)},
		blocked: func() bool { return false }, projects: f.handler.chat.projectTools,
		projectOwnerAllowed: func(current companion.Input) bool { return current.Sender == in.Sender }}
	adapter.SetTrace(in)
	items, err := adapter.List(t.Context())
	if err != nil || !slices.ContainsFunc(items, func(tool mcptools.Tool) bool { return tool.Name == localComfyListTool }) {
		t.Fatalf("application catalog did not expose workstation ComfyUI workflows: %v", err)
	}
	out, err := adapter.Call(t.Context(), localComfyListTool, json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, "Offline smoke") {
		t.Fatalf("application did not execute pure workstation catalog: %s %v", out, err)
	}
	installed := localProjectToolByID(t, f, localComfyPackage)
	if code, result := f.request("PUT", "/api/local/project-tools/comfyui", map[string]any{"action": "disable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatalf("workstation disable failed: %d %#v", code, result)
	}
	items, _ = adapter.List(t.Context())
	if slices.ContainsFunc(items, func(tool mcptools.Tool) bool { return tool.Name == localComfyListTool }) {
		t.Fatal("application retained disabled workstation workflow catalog")
	}
	if out, err := adapter.Call(t.Context(), localComfyListTool, json.RawMessage(`{}`)); err == nil || out != "" {
		t.Fatalf("application called disabled workstation catalog: %s %v", out, err)
	}
}

const localComfyFixturePrompt = "运行登记的离线测试工作流"

func requestLocalComfyJob(t *testing.T, f *localHomeFixture, requestID string) string {
	t.Helper()
	var jobID string
	jobPattern := regexp.MustCompile(`job_id[^a-f0-9]+([a-f0-9]{64})`)
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		round := 0
		return localModelFunc(func(_ context.Context, system string, turns []map[string]string) (string, error) {
			round++
			switch round {
			case 1:
				if !strings.Contains(system, localComfyStartTool) {
					return "", errors.New("ComfyUI missing from schema")
				}
				return localToolCall(localComfyListTool, map[string]any{}), nil
			case 2:
				if !strings.Contains(localModelHistory(turns), "Offline smoke") {
					return "", errors.New("approved workflow missing from discovery")
				}
				return localToolCall(localComfyStartTool, map[string]any{"workflow_id": "smoke", "inputs": map[string]int{"width": 48}}), nil
			default:
				matches := jobPattern.FindStringSubmatch(localModelHistory(turns))
				if len(matches) != 2 {
					return "", errors.New("ComfyUI job receipt missing")
				}
				jobID = matches[1]
				return `{"action":"reply","messages":["已登记任务。"]}`, nil
			}
		}), nil
	}
	if code, out := localChatSend(f, requestID, localComfyFixturePrompt); code != http.StatusOK || out["status"] != "completed" || jobID == "" {
		t.Fatalf("ComfyUI submit chat: %d %#v job=%q", code, out, jobID)
	}
	return jobID
}

func requestLocalComfyStatus(t *testing.T, f *localHomeFixture, requestID, jobID, action string) []string {
	t.Helper()
	var ids []string
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		called := false
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			if !called {
				called = true
				return localToolCall(localComfyJobTool, map[string]string{"job_id": jobID, "action": action}), nil
			}
			ids = projectArtifactIDs(turns)
			return `{"action":"reply","messages":["已检查原任务。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, requestID, "核对原 ComfyUI 任务"); code != http.StatusOK || out["status"] != "completed" {
		t.Fatalf("ComfyUI job chat: %d %#v", code, out)
	}
	return ids
}

func writeLocalComfyChatWorkflow(t *testing.T, root string) {
	t.Helper()
	graph := []byte(`{"1":{"class_type":"EmptyImage","inputs":{"width":32,"height":24,"batch_size":2,"color":16711680}},"9":{"class_type":"SaveImage","inputs":{"images":["1",0],"filename_prefix":"owner-prefix"}}}`)
	writeLocalComfyRegistry(t, root, graph, `[{"id":"width","label":"Width","kind":"integer","minimum":16,"maximum":512,"node":"1","input":"width"}]`)
}

func TestLocalComfyVideoPublishesDecodedOutput(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("local FFmpeg is needed for the video fixture")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("local FFprobe is needed for the video fixture")
	}
	root := t.TempDir()
	clip := filepath.Join(root, "fixture.mp4")
	if data, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=48x24:r=8:d=1", "-c:v", "mpeg4", "-threads", "1", clip).CombinedOutput(); err != nil {
		t.Fatalf("encode local fixture: %v %s", err, data)
	}
	video, err := os.ReadFile(clip)
	if err != nil {
		t.Fatal(err)
	}
	graph := []byte(`{"1":{"class_type":"EmptyImage","inputs":{"width":48,"height":24,"batch_size":8,"color":255}},"2":{"class_type":"CreateVideo","inputs":{"images":["1",0],"fps":8}},"9":{"class_type":"SaveVideo","inputs":{"video":["2",0],"filename_prefix":"owner-prefix","format":"mp4","codec":"h264"}}}`)
	writeLocalComfyRegistry(t, root, graph, `[{"id":"width","label":"Width","kind":"integer","minimum":16,"maximum":512,"node":"1","input":"width"}]`)
	f := localChatFixture(t)
	fish, _, err := fishidentity.New(f.handler.home.dataDir).Bindings()
	if err != nil {
		t.Fatal(err)
	}
	backend := &syntheticLocalComfy{root: root, fish: fish, video: comfyui.MP4{Data: video, SHA256: localComfyDigest(video)}}
	setUpLocalComfyChat(t, f, backend)
	job := requestLocalComfyJob(t, f, "comfy-video-submit")
	backend.state = comfyui.Completed
	ids := requestLocalComfyStatus(t, f, "comfy-video-status", job, "status")
	if len(ids) != 1 || backend.fetches != 1 {
		t.Fatalf("expected one video artifact, got %v after %d readbacks", ids, backend.fetches)
	}
	if !bytes.Equal(localArtifactDownload(t, f, ids[0]), video) {
		t.Fatal("published artifact does not match the job MP4")
	}
	host, err := f.handler.chat.projectTools.forFish(fish)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := host.store.Job(t.Context(), job)
	var receipt localComfyVideoReceipt
	if err != nil || finished.State != "completed" || json.Unmarshal(finished.Result, &receipt) != nil ||
		receipt.Kind != "video" || receipt.Width != 48 || receipt.Height != 24 || receipt.Frames != 8 ||
		receipt.Seconds != 1 || receipt.FPS != 8 || receipt.SHA256 != backend.video.SHA256 {
		t.Fatalf("unexpected decoded video receipt: %+v, %v", receipt, err)
	}
	again := requestLocalComfyStatus(t, f, "comfy-video-status-repeat", job, "status")
	if len(again) != 1 || again[0] != ids[0] || backend.fetches != 1 {
		t.Fatal("completed video receipt was not reused")
	}
}

func TestLocalComfyRechecksOriginalApplicationMemoryBeforeResult(t *testing.T) {
	f, _, _, source := memoryEvidenceFixture(t)
	root := t.TempDir()
	writeLocalComfyChatWorkflow(t, root)
	original, in := memoryEvidenceTools(t, f, "comfy-private-origin")
	memoryEvidenceSearch(t, original, source)
	backend := &syntheticLocalComfy{root: root, fish: in.Account}
	setUpLocalComfyChat(t, f, backend)
	// Call the host implementation directly to exercise its boundary even if
	// the current model tool policy withholds generation after a Douyin read.
	raw, err := original.callLocalComfy(t.Context(), localComfyStartTool, json.RawMessage(`{"workflow_id":"smoke","inputs":{"width":48}}`))
	var submitted struct {
		ID string `json:"job_id"`
	}
	if err != nil || json.Unmarshal([]byte(raw), &submitted) != nil || submitted.ID == "" || backend.submits != 1 {
		t.Fatal("synthetic original was not submitted", raw, err)
	}
	status, _ := json.Marshal(map[string]string{"job_id": submitted.ID, "action": "status"})
	later, _ := memoryEvidenceTools(t, f, "comfy-later-status")
	if value, err := later.Call(t.Context(), localComfyJobTool, status); err != nil || !strings.Contains(value, "running") {
		t.Fatal("authorized original result status unavailable", value, err)
	}
	_, view := f.request("GET", localDouyinWebRoute, nil, false)
	if code, out := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "memory_scope": douyinMemoryWorks, "allow_memory": false, "revision": view["revision"]}, true); code != 200 {
		t.Fatal(code, out)
	}
	backend.state = comfyui.Completed
	if value, err := later.Call(t.Context(), localComfyJobTool, status); err == nil || value != "" || backend.fetches != 0 {
		t.Fatal("revoked original remained readable", value, err, backend.fetches)
	}
	backend.state = comfyui.Running
	cancel, _ := json.Marshal(map[string]string{"job_id": submitted.ID, "action": "cancel"})
	if value, err := later.Call(t.Context(), localComfyJobTool, cancel); err != nil || !strings.Contains(value, "cancelled") || backend.cancels != 1 || backend.submits != 1 {
		t.Fatal("cancel was unavailable or replayed the original", value, err, backend.cancels, backend.submits)
	}
}

func TestLocalComfyChatJobsAndArtifacts(t *testing.T) {
	f := localChatFixture(t)
	root := t.TempDir()
	writeLocalComfyChatWorkflow(t, root)
	fish, _, err := fishidentity.New(f.handler.home.dataDir).Bindings()
	if err != nil || fish == "" {
		t.Fatal("fixture fish missing", err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 48, 24))); err != nil {
		t.Fatal(err)
	}
	backend := &syntheticLocalComfy{root: root, fish: fish, lost: true, image: comfyui.Image{
		Data: data.Bytes(), SHA256: localComfyDigest(data.Bytes()), Width: 48, Height: 24,
	}}
	setUpLocalComfyChat(t, f, backend)
	if backend.starts != 0 {
		t.Fatal("installation or discovery launched ComfyUI")
	}
	jobID := requestLocalComfyJob(t, f, "comfy-submit")
	if code, out := localChatSend(f, "comfy-submit", localComfyFixturePrompt); code != http.StatusOK || out["reused"] != true || backend.submits != 1 {
		t.Fatalf("replayed chat resubmitted: %d %#v calls=%d", code, out, backend.submits)
	}
	backend.state = comfyui.Completed
	backend.failFetch = true
	if ids := requestLocalComfyStatus(t, f, "comfy-status-partial", jobID, "status"); len(ids) != 0 {
		t.Fatalf("uncommitted output was exposed: %v", ids)
	}
	artifactRoot := filepath.Join(f.handler.home.dataDir, "artifacts")
	entries, err := os.ReadDir(artifactRoot)
	if err != nil || len(entries) != 1 || !artifactID(entries[0].Name(), "work-") {
		t.Fatalf("expected one staged immutable copy: %v %v", entries, err)
	}
	if code, _ := f.request("GET", "/api/local/artifacts/"+entries[0].Name(), nil, true); code != http.StatusNotFound {
		t.Fatalf("uncommitted output readable: %d", code)
	}
	host, err := f.handler.chat.projectTools.forFish(fish)
	if err != nil {
		t.Fatal(err)
	}
	record, err := host.store.Job(context.Background(), jobID)
	if err != nil || record.State != "running" || record.RemoteID != backend.prompt.ID {
		t.Fatalf("lost receipt did not reconcile: %+v %v", record, err)
	}
	backend.failFetch = false
	ids := requestLocalComfyStatus(t, f, "comfy-status", jobID, "status")
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("expected two distinct artifact IDs, got %v", ids)
	}
	for _, id := range ids {
		if got := localArtifactDownload(t, f, id); !bytes.Equal(got, data.Bytes()) {
			t.Fatal("ComfyUI download differs from the verified result")
		}
	}
	second := requestLocalComfyJob(t, f, "comfy-submit-cancel")
	if second == jobID {
		t.Fatal("new request reused old job identity")
	}
	before := backend.fetches
	if ids := requestLocalComfyStatus(t, f, "comfy-cancel", second, "cancel"); len(ids) != 0 || backend.cancels != 1 || backend.fetches != before {
		t.Fatalf("cancel performed the wrong operation: ids=%v cancels=%d fetches=%d", ids, backend.cancels, backend.fetches)
	}
	if job, err := host.store.Job(context.Background(), second); err != nil || job.State != "cancelled" || !job.CancelRequested {
		t.Fatalf("cancellation was not durable: %+v %v", job, err)
	}
	third := requestLocalComfyJob(t, f, "comfy-submit-before-exit")
	backend.running = false
	starts := backend.starts
	if ids := requestLocalComfyStatus(t, f, "comfy-status-after-exit", third, "status"); len(ids) != 0 || backend.starts != starts {
		t.Fatalf("query started a new backend: ids=%v starts=%d", ids, backend.starts)
	}
	enabled := localProjectToolByID(t, f, localComfyPackage)
	if code, out := f.request("PUT", "/api/local/project-tools/comfyui", map[string]any{"action": "disable", "revision": localProjectToolRevision(t, enabled)}, true); code != http.StatusOK || backend.stops != 1 {
		t.Fatalf("ComfyUI disable: %d %#v stops=%d", code, out, backend.stops)
	}
	if got := localArtifactDownload(t, f, ids[0]); !bytes.Equal(got, data.Bytes()) {
		t.Fatal("disabling ComfyUI removed an already verified artifact")
	}
}

func TestLocalComfyOwnedWindowsThroughChat(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("AHS_TEST_COMFY_PORTABLE") == "" {
		t.Skip("set AHS_TEST_COMFY_PORTABLE to test the owned Windows CPU service")
	}
	f := localChatFixture(t)
	root := t.TempDir()
	writeLocalComfyChatWorkflow(t, root)
	backend := configureLocalComfyPortable(t, f.handler.home.dataDir, root)
	backend.editableRoot = f.handler.home.root
	setUpLocalComfyChat(t, f, backend)
	jobID := requestLocalComfyJob(t, f, "comfy-native-submit")
	var ids []string
	for i := 0; i < 12 && len(ids) == 0; i++ {
		ids = requestLocalComfyStatus(t, f, fmt.Sprintf("comfy-native-status-%d", i), jobID, "status")
		if len(ids) == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("ComfyUI native batch: %v", ids)
	}
	for _, id := range ids {
		picture, err := png.Decode(bytes.NewReader(localArtifactDownload(t, f, id)))
		if err != nil || picture.Bounds().Dx() != 48 || picture.Bounds().Dy() != 24 {
			t.Fatalf("registered width override did not reach native ComfyUI: %v", err)
		}
		r, g, b, _ := picture.At(0, 0).RGBA()
		if r != 65535 || g != 0 || b != 0 {
			t.Fatalf("wrong native CPU output: %d %d %d", r, g, b)
		}
	}
	enabled := localProjectToolByID(t, f, localComfyPackage)
	if code, out := f.request("PUT", "/api/local/project-tools/comfyui", map[string]any{"action": "disable", "revision": localProjectToolRevision(t, enabled)}, true); code != http.StatusOK {
		t.Fatalf("stop owned ComfyUI: %d %#v", code, out)
	}
	fish, _, err := fishidentity.New(f.handler.home.dataDir).Bindings()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := backend.Info(context.Background(), fish); err != nil || info.Running {
		t.Fatalf("owned process still present after disable: %+v %v", info, err)
	}
}
