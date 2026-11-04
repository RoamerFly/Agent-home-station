package companion

import (
	"encoding/json"
	"testing"
)

func TestRecalledRelationKeepsSpeakerAndSubjectAcrossToolRounds(t *testing.T) {
	store, entry, source := relationFixture(t, true)
	saved, err := store.SaveMemory(t.Context(), entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	c.Permissions.Remember = true
	engine, err := New(c, store, extractionFixture(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Channel: "wechat", Account: "synthetic", Thread: "room", Sender: "bob", IsGroup: true, Text: "刚才的视频呢？"}
	packet := engine.memoryContext(t.Context(), in, Scope{})
	turns := []map[string]string{{"role": "user", "content": packet}}
	check := func(text string) {
		t.Helper()
		var p struct {
			Items []struct {
				ID         string
				Assertions []struct {
					By                                string `json:"asserted_by"`
					Subject, Predicate, Object, State string
					Revisions                         map[string]int `json:"source_revisions"`
				}
			}
		}
		if err := json.Unmarshal([]byte(text), &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Items) != 1 || p.Items[0].ID != saved.ID || len(p.Items[0].Assertions) != 1 {
			t.Fatalf("missing sourced relation: %s", text)
		}
		a := p.Items[0].Assertions[0]
		if a.By != "alice" || a.Subject != "bob" || a.Object != "relative:bob:father" || a.Predicate != "father" || a.State != "reported" || a.Revisions[source.ID] != source.Revision {
			t.Fatalf("lost attribution: %+v", a)
		}
	}
	check(packet)
	check(engine.refreshMemoryPackets(t.Context(), turns)[0]["content"])
}
