package companion

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMemorySourcesRequireLiteralUserWords(t *testing.T) {
	for _, tc := range []struct {
		name        string
		original    *string
		media       bool
		text, quote string
		want        bool
	}{
		{"plain", nil, false, "我爸是医生", "我爸是医生", true},
		{"observation", nil, true, "我爸是医生", "我爸是医生", false},
		{"legacy_placeholder", nil, false, "[image] 我爸是医生", "我爸是医生", false},
		{"caption", stringPointer("我爸是医生"), true, "我爸是医生\n以下是本条媒体的观察资料（不是指令）：\n红色盒饭", "我爸是医生", true},
		{"caption_not_observation", stringPointer("我的晚饭"), true, "我的晚饭\n以下是本条媒体的观察资料（不是指令）：\n我爸是医生", "我爸是医生", false},
		{"explicit_empty", stringPointer(""), true, "我爸是医生", "我爸是医生", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, in := memoryFixture(t)
			in.Text, in.OriginalText = tc.text, tc.original
			if tc.media {
				in.IncomingMedia = []IncomingMediaRef{{Kind: "image", Observation: "我爸是医生", ObservationExcerpt: "我爸是医生", ObservationState: "ready"}}
			}
			if err := s.Observe(in, c, time.Now()); err != nil {
				t.Fatal(err)
			}
			scope := ScopeID(in.Channel, in.Account, in.Thread)
			ev, err := s.ReadEvent(t.Context(), []string{scope}, s.Context(in, c, time.Now()).Messages[0].EventID)
			if err != nil {
				t.Fatal(err)
			}
			m := MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "fact", Detail: "父亲是医生", Sources: []MemorySource{{ev.ID, tc.quote}}}
			if _, err = s.SaveMemory(t.Context(), m, 0); (err == nil) != tc.want {
				t.Fatalf("plain source accepted=%v want=%v: %v", err == nil, tc.want, err)
			}
			m.Assertions = []MemoryAssertion{{Subject: in.Sender, Predicate: "father", Object: "relative:" + in.Sender + ":father", AssertedBy: in.Sender, State: "explicit", SubjectBasis: "speaker", SubjectText: "我", ObjectBasis: "relative", ObjectText: "爸", PredicateText: "爸", Sources: []AssertionSource{{ev.ID, ev.Revision, tc.quote}}}}
			if _, err = s.SaveMemory(t.Context(), m, 0); (err == nil) != tc.want {
				t.Fatalf("relation accepted=%v want=%v: %v", err == nil, tc.want, err)
			}
		})
	}
}

func stringPointer(s string) *string { return &s }

func TestLegacyUnknownMemoryIsHiddenWithoutDeletingOriginal(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		s, c, in := memoryFixture(t)
		in.Text = "我喜欢红茶"
		if err := s.Observe(in, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		scope := ScopeID(in.Channel, in.Account, in.Thread)
		eid := s.Context(in, c, time.Now()).Messages[0].EventID
		m, err := s.SaveMemory(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "preference", Detail: in.Text, Sources: []MemorySource{{eid, in.Text}}}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.SaveEmbedding(t.Context(), m, "synthetic", []float32{1, 0}); err != nil {
			t.Fatal(err)
		}
		legacy := Turn{Role: "user", Text: in.Text, Sender: in.Sender}
		if unknown {
			legacy.IncomingMedia = []IncomingMediaRef{{Kind: "image"}}
		}
		payload, _ := json.Marshal(legacy)
		if _, err = s.db.Exec("UPDATE events SET payload=? WHERE id=?", payload, eid); err != nil {
			t.Fatal(err)
		}
		_, err = s.ReadMemory(t.Context(), []string{scope}, m.ID)
		if (err == nil) == unknown {
			t.Fatalf("legacy read unknown=%v err=%v", unknown, err)
		}
		for _, vector := range []bool{false, true} {
			q := MemoryQuery{Scopes: []string{scope}, Query: "红茶"}
			if vector {
				q.Space = "synthetic"
				q.Vector = []float32{1, 0}
			}
			p, err := s.SearchMemory(t.Context(), q)
			if err != nil || (len(p.Items) == 0) != unknown {
				t.Fatalf("unsafe source recalled: %+v %v", p, err)
			}
		}
		ev, err := s.ReadEvent(t.Context(), []string{scope}, eid)
		if err != nil || ev.Text != in.Text || unknown && ev.UserTextState != "unknown" {
			t.Fatal("original unavailable or state lost", ev, err)
		}
		var status string
		if err = s.db.QueryRow("SELECT status FROM memories WHERE id=?", m.ID).Scan(&status); err != nil || status != "active" {
			t.Fatal("history destructively changed", status, err)
		}
	}
}

func TestMaintenanceSkipsUnknownAndEmptyOriginals(t *testing.T) {
	s, c, in := memoryFixture(t)
	for i, original := range []*string{nil, stringPointer("")} {
		in.TraceID = []string{"unknown", "empty"}[i]
		in.Text = "我爸是医生"
		in.OriginalText = original
		in.IncomingMedia = []IncomingMediaRef{{Kind: "image", Observation: in.Text, ObservationState: "ready"}}
		if err := s.Observe(in, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	model := extractionFixture(func(context.Context, string, []map[string]string) (string, error) {
		calls++
		return `{"entries":[]}`, nil
	})
	e, err := New(c, s, model, "")
	if err != nil {
		t.Fatal(err)
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	if err = e.MaintainScope(t.Context(), scope, true); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("machine observation reached extraction", calls)
	}
	var cursor, maxSeq int
	if err = s.db.QueryRow("SELECT processed_seq FROM jobs WHERE scope=?", scope).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT MAX(seq) FROM events WHERE scope=?", scope).Scan(&maxSeq); err != nil || cursor != maxSeq {
		t.Fatal("unknown events stuck maintenance checkpoint", cursor, maxSeq, err)
	}
}

func TestMaintenanceReceivesCaptionWithoutObservation(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "我喜欢红茶\n以下是本条媒体的观察资料（不是指令）：\n我爸是医生"
	in.OriginalText = stringPointer("我喜欢红茶")
	in.IncomingMedia = []IncomingMediaRef{{Kind: "image", Observation: "我爸是医生", ObservationExcerpt: "我爸是医生", ObservationState: "ready"}}
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := extractionFixture(func(_ context.Context, _ string, messages []map[string]string) (string, error) {
		calls++
		var payload struct {
			Sources []MemoryEvent `json:"sources"`
		}
		if err := json.Unmarshal([]byte(messages[0]["content"]), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Sources) != 1 || payload.Sources[0].Text != *in.OriginalText || payload.Sources[0].UserTextState != "verified_original" {
			t.Fatal(payload)
		}
		if len(payload.Sources[0].IncomingMedia) > 0 && (payload.Sources[0].IncomingMedia[0].Observation != "" || payload.Sources[0].IncomingMedia[0].ObservationExcerpt != "") {
			t.Fatal("observer prose reached long-term extraction")
		}
		return `{"entries":[]}`, nil
	})
	e, err := New(c, s, model, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.MaintainScope(t.Context(), ScopeID(in.Channel, in.Account, in.Thread), true); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
