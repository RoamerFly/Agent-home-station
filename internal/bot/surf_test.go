package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestSurfDoesNotCreateNewShareWhilePreviousDeliveryNeedsHandling(t *testing.T) {
	w, _, _, in := mediaWorker(t)
	in.Surf = true
	w.runtime.Media = append(w.runtime.Media, &mediaDelivery{Input: in, State: "waiting"})
	claims := 0
	w.config.SurfTask = func(time.Time) (*SurfTask, error) { claims++; return nil, nil }
	if w.tickSurf(context.Background()) || claims != 0 {
		t.Fatal("claimed new surf while old delivery pending", claims)
	}
	w.runtime.Media[0].State = "accepted"
	w.tickSurf(context.Background())
	if claims != 1 {
		t.Fatal("surf not resumed after settlement", claims)
	}
}

type quietBoundaryModel struct{ cancelled bool }

func (m *quietBoundaryModel) Chat(ctx context.Context, _ string, _ []map[string]string) (string, error) {
	<-ctx.Done()
	m.cancelled = true
	return "", ctx.Err()
}

func TestSurfInProgressStopsAtQuietBoundaryWithoutSending(t *testing.T) {
	c := companion.Defaults()
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	store, _ := companion.NewStore(t.TempDir())
	model := &quietBoundaryModel{}
	engine, _ := companion.New(c, store, model, "test")
	sender := &fakeSender{}
	records := []Record{}
	worker, err := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"filehelper"}, SurfTask: func(time.Time) (*SurfTask, error) {
		return &SurfTask{Topic: "ocean", QuietDeadline: time.Now().Add(30 * time.Millisecond)}, nil
	}, Record: func(r Record) { records = append(records, r) }}, &fakeSource{}, engine, sender)
	if err != nil {
		t.Fatal(err)
	}
	if !worker.tickSurf(context.Background()) || !model.cancelled || sender.calls != 0 {
		t.Fatal("boundary did not cancel live task")
	}
	found := false
	for _, r := range records {
		if r.Event == "surf_review" && r.Reason == "quiet_hours" && r.Status == "blocked" && r.MessageID != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("interrupted task lacks final state", records)
	}
}
