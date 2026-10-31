package mobile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeBridge struct {
	mu        sync.Mutex
	calls     []string
	args      []map[string]any
	unknown   bool
	invalid   bool
	observes  int
	onObserve func(int)
}

func TestDevicePermissionRevokedDuringPlanning(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	c, b := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		allowed.Store(false)
		return Step{Action: "tap", Arguments: map[string]any{"x": 1, "y": 1}}, nil
	})
	c.SetAuthorizer(func(Source) bool { return allowed.Load() })
	task, err := c.Start(Source{Admin: true, MessageID: "revoked"}, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, task.ID, "stopped")
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.args) != 0 {
		t.Fatal("action after trust revocation")
	}
}

func TestDevicePermissionRevokedDuringPlanningCannotFinish(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	c, _ := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		allowed.Store(false)
		return Step{Action: "finish", Summary: "stale success"}, nil
	})
	c.SetAuthorizer(func(Source) bool { return allowed.Load() })
	task, err := c.Start(Source{Admin: true, MessageID: "revoked-finish"}, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, c, task.ID, "stopped")
	if got.Reason != "device_permission_changed" || got.Result != "" {
		t.Fatalf("revoked planner completed task: %#v", got)
	}
}

func TestDeviceCancelRechecksCurrentAuthorizer(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	entered := make(chan struct{})
	c, _ := mobileFixture(t, func(ctx context.Context, _ Task, _ Observation) (Step, error) {
		close(entered)
		<-ctx.Done()
		return Step{}, ctx.Err()
	})
	c.SetAuthorizer(func(Source) bool { return allowed.Load() })
	task, err := c.Start(Source{Admin: true, MessageID: "cancel-revoked"}, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	allowed.Store(false)
	if err = c.Cancel(task.ID, Source{Admin: true, MessageID: "cancel-revoked"}); err == nil {
		t.Fatal("revoked caller cancelled durable task")
	}
	if err = c.StopAll(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, task.ID, "cancelled")
}

func TestDeviceCancelBindsOriginalRequesterOrUnscopedManagement(t *testing.T) {
	entered := make(chan struct{})
	c, _ := mobileFixture(t, func(ctx context.Context, _ Task, _ Observation) (Step, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-ctx.Done()
		return Step{}, ctx.Err()
	})
	// This mirrors the production adapter: newly started tasks require Admin,
	// but a normal chat cancellation is deliberately presented without it.
	c.SetAuthorizer(func(source Source) bool { return source.Admin })
	original := Source{Admin: true, Account: "account", Thread: "room", Sender: "owner", MessageID: "cancel-owner"}
	task, err := c.Start(original, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	// The adapter may not classify a normal chat cancel as Admin, but exact
	// requester identity remains sufficient to stop its own task.
	if err = c.Cancel(task.ID, Source{Account: "account", Thread: "room", Sender: "owner", MessageID: "cancel-message"}); err != nil {
		t.Fatalf("original requester could not cancel: %v", err)
	}
	waitState(t, c, task.ID, "cancelled")

	entered = make(chan struct{})
	other, err := c.Start(Source{Admin: true, Account: "account", Thread: "room", Sender: "owner", MessageID: "cancel-other"}, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err = c.Cancel(other.ID, Source{Admin: true, Account: "account", Thread: "room", Sender: "member"}); err == nil {
		t.Fatal("in-conversation admin bit bypassed original requester")
	}
	if err = c.Cancel(other.ID, Source{Admin: true}); err != nil {
		t.Fatalf("unscoped management cancel rejected: %v", err)
	}
	waitState(t, c, other.ID, "cancelled")
}

func TestDeviceManagementCanCancelAfterOriginalRequesterIsRevoked(t *testing.T) {
	var originalAllowed atomic.Bool
	originalAllowed.Store(true)
	entered := make(chan struct{})
	c, _ := mobileFixture(t, func(ctx context.Context, _ Task, _ Observation) (Step, error) {
		close(entered)
		<-ctx.Done()
		return Step{}, ctx.Err()
	})
	c.SetAuthorizer(func(source Source) bool {
		if !source.Admin {
			return false
		}
		if source.Account == "" && source.Thread == "" && source.Sender == "" {
			return true // separately authenticated local management surface
		}
		return source.Account == "account" && source.Thread == "room" && source.Sender == "owner" && originalAllowed.Load()
	})
	original := Source{Admin: true, Account: "account", Thread: "room", Sender: "owner", MessageID: "orphaned-task"}
	task, err := c.Start(original, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	originalAllowed.Store(false)
	if err := c.Cancel(task.ID, Source{Account: "account", Thread: "room", Sender: "owner", MessageID: "chat-cancel"}); err == nil {
		t.Fatal("revoked chat requester cancelled task")
	}
	if err := c.Cancel(task.ID, Source{Admin: true}); err != nil {
		t.Fatalf("authenticated management surface could not cancel orphaned task: %v", err)
	}
	waitState(t, c, task.ID, "cancelled")
}
func TestDeviceLoginBlockIsNotCompleted(t *testing.T) {
	c, _ := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		return Step{Action: "blocked", Summary: "需要本人登录，尚未完成浏览"}, nil
	})
	task, err := c.Start(Source{Admin: true, MessageID: "blocked"}, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, c, task.ID, "needs_review")
	if got.Reason != "device_user_action_required" || got.Result == "" || got.Steps != 0 {
		t.Fatal(got)
	}
}

func (b *fakeBridge) Close() error { return nil }
func (b *fakeBridge) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, name)
	if name == "devices" {
		return `{"devices":[{"id":"lab","state":"device"}]}`, nil
	}
	if name == "act" {
		var a map[string]any
		_ = json.Unmarshal(args, &a)
		b.args = append(b.args, a)
		if b.unknown {
			return "", errors.New("connection_lost")
		}
		return `{"state":"submitted"}`, nil
	}
	b.observes++
	if b.onObserve != nil {
		b.onObserve(b.observes)
	}
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 10, 20)))
	if b.invalid {
		pngData.Reset()
		pngData.WriteString("invalid PNG with sufficient length")
	}
	snapshot := `{"id":"obs-0123456789abcdef0123456789abcdef","device_id":"lab","package":"test.app","width":10,"height":20,"nodes":[]}`
	raw, _ := json.Marshal(map[string]any{"text": snapshot, "media": []map[string]string{{"mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(pngData.Bytes())}}})
	return string(raw), nil
}

func TestDeviceRevokedDuringFirstObservationNeverReachesPlanner(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	var planned atomic.Int32
	c, b := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		planned.Add(1)
		return Step{Action: "finish"}, nil
	})
	c.SetAuthorizer(func(Source) bool { return allowed.Load() })
	b.onObserve = func(call int) {
		if call == 1 {
			allowed.Store(false)
		}
	}
	task, err := c.Start(Source{Admin: true, MessageID: "revoke-first-observe"}, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, c, task.ID, "stopped")
	if planned.Load() != 0 || got.Reason != "device_permission_changed" {
		t.Fatalf("revoked first observation reached planner: planned=%d task=%#v", planned.Load(), got)
	}
}

func TestDeviceRevokedDuringFreshObservationNeverActs(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	var planned atomic.Int32
	c, b := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		planned.Add(1)
		return Step{Action: "tap", Arguments: map[string]any{"x": 1, "y": 1}}, nil
	})
	c.SetAuthorizer(func(Source) bool { return allowed.Load() })
	b.onObserve = func(call int) {
		if call == 2 { // fresh pre-action observation
			allowed.Store(false)
		}
	}
	task, err := c.Start(Source{Admin: true, MessageID: "revoke-fresh-observe"}, "lab", "view")
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, c, task.ID, "stopped")
	if planned.Load() != 1 || got.Reason != "device_permission_changed" {
		t.Fatalf("fresh revocation task=%#v planned=%d", got, planned.Load())
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.args) != 0 {
		t.Fatalf("action after fresh-observation revocation: %#v", b.args)
	}
}
func mobileFixture(t *testing.T, p Planner) (*Control, *fakeBridge) {
	t.Helper()
	c, e := Open(t.TempDir(), p)
	if e != nil {
		t.Fatal(e)
	}
	b := &fakeBridge{}
	c.factory = func(Settings) (Bridge, error) { return b, nil }
	s := Defaults()
	s.Enabled = true
	s.Devices = []string{"lab"}
	s.Actions = []string{"tap"}
	s.Bridge.Command = "synthetic-not-executed"
	if e = c.Save(s); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, b
}
func waitState(t *testing.T, c *Control, id string, want string) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := c.Get(id)
		if task.State == want {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _ := c.Get(id)
	t.Fatalf("state %s, expected %s (%s)", task.State, want, task.Reason)
	return Task{}
}
func TestDeviceTaskObservationBeforeAndAfterAction(t *testing.T) {
	c, b := mobileFixture(t, func(_ context.Context, task Task, o Observation) (Step, error) {
		if len(o.PNG) == 0 {
			t.Error("missing actual image")
		}
		if task.Steps == 0 {
			return Step{Action: "tap", Arguments: map[string]any{"x": 1, "y": 2, "device_id": "attacker", "request_key": "replay"}}, nil
		}
		return Step{Action: "finish", Summary: "已根据新画面核对"}, nil
	})
	task, e := c.Start(Source{Admin: true, MessageID: "one"}, "lab", "检查页面")
	if e != nil {
		t.Fatal(e)
	}
	got := waitState(t, c, task.ID, "completed")
	if got.Steps != 1 || got.Result == "" {
		t.Fatal(got)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) != 4 || b.calls[0] != "observe" || b.calls[1] != "observe" || b.calls[2] != "act" || b.calls[3] != "observe" {
		t.Fatal(b.calls)
	}
	if b.args[0]["device_id"] != "lab" || b.args[0]["request_key"] == "replay" {
		t.Fatal(b.args)
	}
}
func TestDeviceTaskCancelDeduplicateAndLock(t *testing.T) {
	entered := make(chan struct{})
	c, b := mobileFixture(t, func(ctx context.Context, _ Task, _ Observation) (Step, error) {
		close(entered)
		<-ctx.Done()
		return Step{Action: "tap"}, nil
	})
	src := Source{Admin: true, MessageID: "same"}
	task, e := c.Start(src, "lab", "只观察")
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	again, e := c.Start(src, "lab", "只观察")
	if e != nil || again.ID != task.ID {
		t.Fatal("duplicate task", e)
	}
	queued, e := c.Start(Source{Admin: true, MessageID: "other"}, "lab", "另一个")
	if e != nil || queued.State != "queued" {
		t.Fatal("durable queue", e)
	}
	if e = c.Cancel(task.ID, Source{Account: "other"}); e == nil {
		t.Fatal("scope bypass")
	}
	if e = c.StopAll(); e != nil {
		t.Fatal(e)
	}
	_ = c.Close()
	waitState(t, c, task.ID, "cancelled")
	waitState(t, c, queued.ID, "cancelled")
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.args) != 0 {
		t.Fatal("action after cancellation")
	}
}
func TestDeviceTaskUnknownNotReplayedAndRestartReview(t *testing.T) {
	c, b := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		return Step{Action: "tap", Arguments: map[string]any{"x": 1, "y": 1}}, nil
	})
	b.unknown = true
	task, e := c.Start(Source{Admin: true, MessageID: "unknown"}, "lab", "点击")
	if e != nil {
		t.Fatal(e)
	}
	got := waitState(t, c, task.ID, "needs_review")
	if got.Reason != "device_action_outcome_unknown" {
		t.Fatal(got)
	}
	_ = c.Close()
	b.mu.Lock()
	if len(b.args) != 1 {
		t.Fatal(b.args)
	}
	b.mu.Unlock()
	got.State = "running"
	if e = write(filepath.Join(c.dir, "tasks.json"), map[string]Task{got.ID: got}); e != nil {
		t.Fatal(e)
	}
	restarted, e := Open(c.dir, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	got, _ = restarted.Get(got.ID)
	if got.State != "needs_review" || got.Reason != "device_worker_interrupted_reobserve_required" {
		t.Fatal(got)
	}
}
func TestDeviceTaskPermissionAndMalformedImage(t *testing.T) {
	c, b := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) { return Step{Action: "tap"}, nil })
	if _, e := c.Start(Source{MessageID: "untrusted"}, "lab", "goal"); e == nil {
		t.Fatal("untrusted start")
	}
	s := c.Settings()
	s.Actions = nil
	if e := c.Save(s); e != nil {
		t.Fatal(e)
	}
	task, e := c.Start(Source{Admin: true, MessageID: "denied"}, "lab", "goal")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, c, task.ID, "stopped")
	_ = c.Close()
	c2, b2 := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		t.Error("invalid image reached model")
		return Step{}, nil
	})
	b2.invalid = true
	task, e = c2.Start(Source{Admin: true, MessageID: "invalid"}, "lab", "goal")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, c2, task.ID, "needs_review")
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.args) != 0 {
		t.Fatal("disallowed act")
	}
}
func TestDevicePrivateScreenshotAndConfiguration(t *testing.T) {
	c, _ := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		return Step{Action: "finish", Summary: "done"}, nil
	})
	task, e := c.Start(Source{Admin: true, MessageID: "view"}, "lab", "view")
	if e != nil {
		t.Fatal(e)
	}
	got := waitState(t, c, task.ID, "completed")
	if _, e = c.Screenshot(task.ID, got.ObservationID); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Screenshot("other", got.ObservationID); e == nil {
		t.Fatal("screenshot ownership")
	}
	if _, e = c.Screenshot(task.ID, "../settings.json"); e == nil {
		t.Fatal("path escape")
	}
	settings := c.Settings()
	settings.Bridge.Env = map[string]string{"KEY": "synthetic"}
	if e = c.Save(settings); e == nil {
		t.Fatal("credentials accepted")
	}
	if _, e = os.Stat(filepath.Join(c.dir, "observations", got.ObservationID+".png")); e != nil {
		t.Fatal(e)
	}
}
