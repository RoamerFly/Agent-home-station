package admin

import "testing"

func TestContinuationTaskID(t *testing.T) {
	if got := continuationTaskID("task-stable-id-20261122T035119.006636200"); got != "stable-id" {
		t.Fatalf("task ID = %q", got)
	}
	for _, trace := range []string{"task-stable-id", "task--20261122T035119.006636200", "music-result-stable-id-035119.006636200", "task-stable-id-20261122T999999.006636200"} {
		if got := continuationTaskID(trace); got != "" {
			t.Fatalf("accepted unrelated or invalid trace %q: %q", trace, got)
		}
	}
}
