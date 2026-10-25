package companion

import (
	"testing"
	"time"
)

func TestQuietDeadlineUsesConfiguredTimezoneAndLocalDay(t *testing.T) {
	c := Defaults()
	for _, pair := range [][2]string{{"2026-10-25T22:59:59+08:00", "2026-10-25T23:00:00+08:00"}, {"2026-10-25T08:00:00+08:00", "2026-10-25T23:00:00+08:00"}, {"2026-10-25T23:00:00+08:00", "2026-10-25T23:00:00+08:00"}} {
		now, _ := time.Parse(time.RFC3339, pair[0])
		want, _ := time.Parse(time.RFC3339, pair[1])
		if got := c.QuietDeadline(now); !got.Equal(want) {
			t.Fatal(pair, got)
		}
	}
	c.QuietStart = "09:00"
	c.QuietEnd = "10:00"
	now, _ := time.Parse(time.RFC3339, "2026-10-25T11:00:00+08:00")
	want, _ := time.Parse(time.RFC3339, "2026-10-26T09:00:00+08:00")
	if got := c.QuietDeadline(now); !got.Equal(want) {
		t.Fatal(got)
	}
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	if !c.QuietDeadline(now).IsZero() {
		t.Fatal("equal hours should not impose a deadline")
	}
	c.Timezone = "America/New_York"
	c.QuietStart = "23:00"
	c.QuietEnd = "08:00"
	now, _ = time.Parse(time.RFC3339, "2026-10-31T23:30:00-04:00")
	if !c.QuietDeadline(now).Equal(now) {
		t.Fatal("already quiet should stop now")
	}
	now, _ = time.Parse(time.RFC3339, "2026-11-01T08:00:00-05:00")
	want, _ = time.Parse(time.RFC3339, "2026-11-01T23:00:00-05:00")
	if got := c.QuietDeadline(now); !got.Equal(want) {
		t.Fatal("DST wall time drift", got)
	}
}
