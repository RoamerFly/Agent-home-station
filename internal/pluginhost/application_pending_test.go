package pluginhost

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestPendingApplicationJobsFiltersBeforePagination(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "fish-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ids := []string{}
	for n := 0; n < 8; n++ {
		job := applicationJobFixture()
		job.Tool = "interaction"
		job.Context.RequestKey = fmt.Sprintf("pending-%d", n)
		origin := "wechat_surf"
		if n == 0 {
			origin = "local"
		}
		if n == 1 {
			job.Context.Requester = "other-owner"
		}
		job.Arguments, _ = json.Marshal(map[string]any{"draft": map[string]any{"origin": map[string]string{"kind": origin}}})
		created, _, err := store.CreateApplicationJob(t.Context(), "douyin", job)
		if err != nil {
			t.Fatal(err)
		}
		if n == 2 {
			continue
		} // unsubmitted draft
		claimed, err := store.ClaimJob(t.Context(), created.ID, created.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if n == 3 {
			if _, err := store.FinishJob(t.Context(), claimed.ID, claimed.Revision, "failed", nil); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if n == 4 {
			if _, err := store.RequestJobCancel(t.Context(), claimed.ID, claimed.Revision); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if n >= 5 {
			ids = append(ids, claimed.ID)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dir, "fish-1")
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.PendingApplicationJobs(t.Context(), "local-owner", "wechat_surf", 0, 2)
	if err != nil || len(page.Items) != 2 || !page.HasMore || page.Items[0].ID != ids[0] || page.Items[1].ID != ids[1] {
		t.Fatal(page, err)
	}
	more, err := store.PendingApplicationJobs(t.Context(), "local-owner", "wechat_surf", page.Next, 2)
	if err != nil || len(more.Items) != 1 || more.HasMore || more.Items[0].ID != ids[2] {
		t.Fatal(more, err)
	}
}
