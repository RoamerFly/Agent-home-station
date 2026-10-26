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
	mu      sync.Mutex
	calls   []string
	args    []map[string]any
	unknown bool
	invalid bool
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
	if len(b.calls) != 3 || b.calls[0] != "observe" || b.calls[1] != "act" || b.calls[2] != "observe" {
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
	if _, e = c.Start(Source{Admin: true, MessageID: "other"}, "lab", "另一个"); e == nil {
		t.Fatal("device lock missing")
	}
	if e = c.Cancel(task.ID, Source{Account: "other"}); e == nil {
		t.Fatal("scope bypass")
	}
	if e = c.Cancel(task.ID, Source{Admin: true}); e != nil {
		t.Fatal(e)
	}
	_ = c.Close()
	waitState(t, c, task.ID, "cancelled")
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
