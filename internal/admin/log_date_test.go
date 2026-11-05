package admin

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestLogDateIncludesArchiveAndShanghaiBoundaries(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.journal = []LogEntry{
		{ID: "today", Time: "2026-10-29T16:00:00Z", Event: "message_received"},
		{ID: "tomorrow", Time: "2026-10-30T16:00:00Z", Event: "message_received"},
		{ID: "yesterday", Time: "2026-10-29T15:59:59Z", Event: "message_received"},
	}
	for i := 0; i < 12; i++ {
		_, err := f.manager.historyArchive().Put("activity", activityScope(), []LogEntry{{ID: fmt.Sprintf("archive-%d", i), Time: "2026-10-30T08:00:00Z", Event: "message_received"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	pages := 0
	for {
		code, data, _ := f.request("GET", "/api/logs?date=2026-10-30&after="+after, nil, false, false)
		if code != 200 {
			t.Fatal(code, string(data))
		}
		var page struct {
			Items    []LogEntry `json:"items"`
			Next     string     `json:"next"`
			Complete bool       `json:"complete"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Items {
			if seen[entry.ID] {
				t.Fatal("repeated page", entry.ID)
			}
			seen[entry.ID] = true
			if entry.Category != "received" {
				t.Fatal("missing category")
			}
		}
		pages++
		if page.Complete {
			break
		}
		if pages > 3 || page.Next == "" {
			t.Fatal("bad continuation")
		}
		after = page.Next
	}
	if pages != 2 || len(seen) != 13 || !seen["today"] || seen["yesterday"] || seen["tomorrow"] {
		t.Fatal("date coverage", pages, seen)
	}
	for _, query := range []string{"date=2026-02-30", "date=", "date=2026-10-30&after=../other"} {
		if code, _, _ := f.request("GET", "/api/logs?"+query, nil, false, false); code != 400 {
			t.Fatal(query, code)
		}
	}
}
