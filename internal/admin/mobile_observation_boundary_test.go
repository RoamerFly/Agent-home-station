package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	mobile "wechat-bot/internal/devicespec"
)

// Observation controls are management actions: a passive overview is safe to
// read, but a browser without an authenticated CSRF-bound session must never
// pause, resume, take over, or cause a device observation.
func TestMobileObservationControlsRequireSessionAndCSRF(t *testing.T) {
	f := setup(t)
	paths := []string{
		"/api/mobile/overview/synthetic/pause",
		"/api/mobile/overview/synthetic/takeover",
		"/api/mobile/overview/synthetic/resume",
		"/api/mobile/overview/synthetic/observe",
	}
	if code, _, _ := f.request("GET", "/api/mobile/overview", nil, false, false); code != 401 {
		t.Fatalf("anonymous overview status=%d", code)
	}
	for _, path := range paths {
		if code, _, _ := f.request("POST", path, map[string]string{}, true, false); code != 401 {
			t.Fatalf("anonymous %s status=%d", path, code)
		}
	}

	f.login()
	for _, path := range paths {
		if code, _, _ := f.request("POST", path, map[string]string{}, true, false); code != 403 {
			t.Fatalf("csrf bypass %s status=%d", path, code)
		}
	}
}

// A failed probe or an owner pause must never replace a previously verified
// screenshot's package/capture metadata. The UI may say disconnected or
// paused, but must still label this picture as historical rather than invent a
// new live observation.
func TestMobileObservationPausePreservesLastVerifiedScreenshotMetadata(t *testing.T) {
	f := setup(t)
	service := f.manager.mobile.(*mobileWorkflowService)
	settings := service.Settings()
	settings.Enabled = true
	settings.Devices = []string{"synthetic"}
	if err := service.Save(settings); err != nil {
		t.Fatal(err)
	}
	captured := time.Date(2026, time.November, 5, 8, 0, 0, 123000000, time.UTC)
	task := mobile.Task{ID: "historical-observation", RequestHash: "synthetic-historical", DeviceID: "synthetic", State: "completed", Source: mobile.Source{Admin: true}, ObservationID: "obs-historical", Created: captured, Updated: captured}
	observation := mobile.Observation{ID: task.ObservationID, DeviceID: task.DeviceID, Package: "com.example.lastseen", CapturedAt: captured}
	snapshot, store, err := service.load(true)
	if err != nil {
		t.Fatal(err)
	}
	taskRaw, _ := json.Marshal(task)
	recordRaw, _ := json.Marshal(mobileWorkflowRecord{State: mustJSON(map[string]mobile.Observation{"observation": observation})})
	if err := service.commit(snapshot, store, map[string]json.RawMessage{mobileTaskRecordKey(task.ID): taskRaw, mobileWorkflowKey(task.ID): recordRaw}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.SetObservationPaused("synthetic", true, true); err != nil {
		t.Fatal(err)
	}
	service.recordObservationProbe("synthetic", "disconnected")
	overview := service.Overview()
	if len(overview.Devices) != 1 {
		t.Fatal(overview)
	}
	got := overview.Devices[0]
	if !got.Paused || !got.Takeover || got.ObservedAt != captured || got.CurrentApp != observation.Package || got.TaskID != task.ID || got.ObservationID != observation.ID || got.ScreenshotURI == "" {
		t.Fatalf("pause/probe changed historical observation: %#v", got)
	}

	if _, err := service.SetObservationPaused("synthetic", false, false); err != nil {
		t.Fatal(err)
	}
	got = service.Overview().Devices[0]
	if got.Paused || got.Takeover || got.ObservedAt != captured || got.CurrentApp != observation.Package {
		t.Fatalf("resume changed historical observation: %#v", got)
	}
}

// Corrupt control metadata is never interpreted as "not paused". This is
// deliberately fail-closed: a repairable local-state error cannot accidentally
// grant a device action or an extra screenshot.
func TestMobileObservationMalformedControlFailsClosed(t *testing.T) {
	if _, err := decodeMobileObservationControl(json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed control state was treated as an implicit resume")
	}
	// A control record missing its optional map is still a valid historic
	// record; the decoder must initialize it without granting any device.
	control, err := decodeMobileObservationControl(json.RawMessage(`{}`))
	if err != nil || control.Devices == nil || len(control.Devices) != 0 {
		t.Fatalf("valid empty control was not safely normalized: %#v, %v", control, err)
	}
}

func seedObservationOnlyTask(t *testing.T, service *mobileWorkflowService, id string) mobile.Task {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := mobile.Task{ID: id, RequestHash: "synthetic-" + id, DeviceID: "synthetic", Goal: "只读查看设备当前画面", State: "running", Source: mobile.Source{Admin: true}, Created: now, Updated: now}
	snapshot, store, err := service.load(true)
	if err != nil {
		t.Fatal(err)
	}
	taskRaw, _ := json.Marshal(task)
	recordRaw, _ := json.Marshal(mobileWorkflowRecord{State: json.RawMessage(`{"observation_only":true}`)})
	if err = service.commit(snapshot, store, map[string]json.RawMessage{mobileTaskRecordKey(id): taskRaw, mobileWorkflowKey(id): recordRaw}); err != nil {
		t.Fatal(err)
	}
	return task
}

// A one-shot observation has no resumable planner state. After a process
// restart it must become cancelled rather than entering the normal workflow.
// Conversely, a live in-process observation has an active cancellation handle
// and must not be mistaken for a restart remnant if another task first calls
// recover().
func TestMobileObservationRecoveryCancelsOnlyDetachedOneShot(t *testing.T) {
	f := setup(t)
	service := f.manager.mobile.(*mobileWorkflowService)
	settings := service.Settings()
	settings.Enabled = true
	settings.Devices = []string{"synthetic"}
	if err := service.Save(settings); err != nil {
		t.Fatal(err)
	}

	detached := seedObservationOnlyTask(t, service, "device-observe-detached")
	if err := service.recover(); err != nil {
		t.Fatal(err)
	}
	got, ok := service.Get(detached.ID)
	if !ok || got.State != "cancelled" || got.Reason != "device_observation_interrupted" {
		t.Fatalf("detached one-shot was resumed or not cancelled: %#v", got)
	}

	live := seedObservationOnlyTask(t, service, "device-observe-live")
	service.mu.Lock()
	service.recovered = false
	service.active[live.ID] = func() {}
	service.mu.Unlock()
	defer func() {
		service.mu.Lock()
		delete(service.active, live.ID)
		service.mu.Unlock()
	}()
	if err := service.recover(); err != nil {
		t.Fatal(err)
	}
	got, ok = service.Get(live.ID)
	if !ok || got.State != "running" {
		t.Fatalf("live one-shot was mistaken for restart remnant: %#v", got)
	}
}

// Pause/takeover must be durable authorization boundaries, not presentation
// toggles. Resume is the only normal route which makes a device eligible for a
// new task again. This fixture has no device package or bridge, so the test is
// also evidence that merely loading the overview never tries to start one.
func TestMobileObservationPauseAndExplicitResumeBoundaries(t *testing.T) {
	f := setup(t)
	settings := f.manager.mobile.Settings()
	settings.Enabled = true
	settings.Devices = []string{"synthetic"}
	if err := f.manager.mobile.Save(settings); err != nil {
		t.Fatal(err)
	}

	f.login()
	if code, body, _ := f.request("GET", "/api/mobile/overview", nil, false, false); code != 200 {
		t.Fatalf("passive overview status=%d body=%s", code, body)
	} else if strings.Contains(string(body), "screenshot_uri") {
		t.Fatalf("passive overview invented a screenshot: %s", body)
	}

	pause := "/api/mobile/overview/synthetic/pause"
	if code, body, _ := f.request("POST", pause, map[string]string{}, true, true); code != 200 {
		t.Fatalf("pause status=%d body=%s", code, body)
	}
	if _, err := f.manager.mobile.Start(mobile.Source{Admin: true, MessageID: "owner-start"}, "synthetic", "read only"); err == nil || err.Error() != "device_observation_paused" {
		t.Fatalf("paused device accepted a new task: %v", err)
	}
	if code, _, _ := f.request("POST", "/api/mobile/overview/synthetic/observe", map[string]string{}, true, true); code != 409 {
		t.Fatalf("paused device observation status=%d", code)
	}

	takeover := "/api/mobile/overview/synthetic/takeover"
	if code, body, _ := f.request("POST", takeover, map[string]string{}, true, true); code != 200 {
		t.Fatalf("takeover status=%d body=%s", code, body)
	}
	if _, err := f.manager.mobile.Start(mobile.Source{Admin: true, MessageID: "owner-start-after-takeover"}, "synthetic", "read only"); err == nil || err.Error() != "device_observation_paused" {
		t.Fatalf("taken-over device accepted a new task: %v", err)
	}

	if code, body, _ := f.request("POST", "/api/mobile/overview/synthetic/resume", map[string]string{}, true, true); code != 200 {
		t.Fatalf("resume status=%d body=%s", code, body)
	}
	if _, err := f.manager.mobile.Start(mobile.Source{Admin: true, MessageID: "owner-start-after-resume"}, "synthetic", "read only"); err != nil && err.Error() == "device_observation_paused" {
		t.Fatalf("resume did not clear pause: %v", err)
	}

	code, body, _ := f.request("GET", "/api/mobile/overview", nil, false, false)
	if code != 200 {
		t.Fatalf("overview after resume status=%d body=%s", code, body)
	}
	var overview mobileOverview
	if err := json.Unmarshal(body, &overview); err != nil || len(overview.Devices) != 1 || overview.Devices[0].Paused || overview.Devices[0].Takeover {
		t.Fatalf("resume state=%#v err=%v", overview, err)
	}
}
