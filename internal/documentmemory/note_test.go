package documentmemory

import (
	"strings"
	"testing"
)

func TestNoteUpdatesKeepRequirementsAndRejectStaleRevision(t *testing.T) {
	old, err := (Input{Summary: "创建模板", Requirements: "留空手写部分", Parents: []string{"original"}}).Update(nil, "request-one")
	if err != nil {
		t.Fatal(err)
	}
	next, err := (Input{Revision: 1, Summary: "标题居中"}).Update(old, "request-two")
	if err != nil || next.Requirements != old.Requirements || len(next.Parents) != 1 || next.SourceMessage != "request-two" {
		t.Fatal(next, err)
	}
	if _, err = (Input{Revision: 1, Summary: "过期修改"}).Update(next, "stale"); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err = (Input{Summary: strings.Repeat("x", 8193)}).Update(nil, "large"); err == nil {
		t.Fatal("oversized full text accepted as compact note")
	}
	page, err := Page([]map[string]any{{"document_note": old}, {"document_note": next}}, Input{Limit: 1})
	if err != nil || page["next_offset"] != 1 || page["complete"] != false {
		t.Fatal(page, err)
	}
}
