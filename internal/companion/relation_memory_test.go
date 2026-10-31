package companion

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func relationFixture(t *testing.T, addressee bool) (*Store, MemoryEntry, MemoryEvent) {
	t.Helper()
	s, c, in := memoryFixture(t)
	in.Text = "你爸刚视频，刚挂掉"
	if addressee {
		in.Addressees = []string{"bob"}
	}
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	p, err := s.SearchMemory(context.Background(), MemoryQuery{Scopes: []string{scope}, Originals: true})
	if err != nil || len(p.Items) != 1 {
		t.Fatal(p, err)
	}
	ev := *p.Items[0].Event
	a := MemoryAssertion{Subject: "bob", Predicate: "father", Object: "relative:bob:father", AssertedBy: "alice", State: "reported", SubjectBasis: "addressee", SubjectText: "你", ObjectBasis: "relative", ObjectText: "爸", PredicateText: "爸", Sources: []AssertionSource{{ev.ID, ev.Revision, in.Text}}}
	return s, MemoryEntry{Scope: scope, Subject: "bob", Kind: "episode", Detail: "alice对bob转述其父亲刚视频", Sources: []MemorySource{{ev.ID, in.Text}}, Assertions: []MemoryAssertion{a}}, ev
}
func TestRelationMemoryRequiresAddresseeAndRevision(t *testing.T) {
	for _, known := range []bool{false, true} {
		s, m, _ := relationFixture(t, known)
		_, err := s.SaveMemory(t.Context(), m, 0)
		if known && err != nil || !known && err == nil {
			t.Fatal(known, err)
		}
	}
	s, m, _ := relationFixture(t, true)
	m.Assertions[0].Sources[0].EventRevision++
	if _, err := s.SaveMemory(t.Context(), m, 0); err == nil {
		t.Fatal("stale assertion accepted")
	}
}
func TestRelationMemoryPreservesReportedAttribution(t *testing.T) {
	s, m, _ := relationFixture(t, true)
	m.Assertions[0].State = "explicit"
	if _, err := s.SaveMemory(t.Context(), m, 0); err == nil {
		t.Fatal("reported fact became self statement")
	}
	m.Assertions[0].State = "reported"
	saved, err := s.SaveMemory(t.Context(), m, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved.Status = "deleted"
	if _, err = s.SaveMemory(t.Context(), saved, saved.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadEvent(t.Context(), []string{m.Scope}, m.Sources[0].EventID); err == nil {
		t.Fatal("forgotten assertion source returned")
	}
}
func TestRelationEventReadReturnsSourceIdentity(t *testing.T) {
	s, m, ev := relationFixture(t, true)
	got, err := s.ReadEvent(t.Context(), []string{m.Scope}, ev.ID)
	if err != nil || len(got.Addressees) != 1 || got.Addressees[0] != "bob" {
		t.Fatal(got, err)
	}
}
func TestAdminCorrectionIsIndependentAttributedSource(t *testing.T) {
	s, m, ev := relationFixture(t, false)
	a := MemoryAssertion{Subject: "bob", Predicate: "father", Object: "relative:bob:father", SubjectBasis: "speaker", SubjectText: "我", ObjectBasis: "relative", ObjectText: "爸", PredicateText: "爸"}
	detail := "这句话对我说，你爸指我的爸爸"
	saved, err := s.SaveSourcedAdminCorrection(t.Context(), m.Scope, "bob", ev.ID, ev.Revision, detail, []MemoryAssertion{a})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Assertions[0].State != "corrected" || saved.Assertions[0].AssertedBy != "bob" {
		t.Fatal(saved)
	}
	source, err := s.ReadEvent(t.Context(), []string{m.Scope}, saved.Sources[0].EventID)
	if err != nil || source.Role != "admin_correction" || source.QuotedSource == nil {
		t.Fatal(source, err)
	}
	replay, err := s.SaveSourcedAdminCorrection(t.Context(), m.Scope, "bob", ev.ID, ev.Revision, detail, []MemoryAssertion{a})
	if err != nil || replay.ID != saved.ID {
		t.Fatal(replay, err)
	}
}

func TestRevisedRelationCanStillBeForgotten(t *testing.T) {
	s, m, ev := relationFixture(t, true)
	saved, err := s.SaveMemory(t.Context(), m, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE events SET revision=revision+1,text='修订后的原文' WHERE id=?", ev.ID); err != nil {
		t.Fatal(err)
	}
	saved.Status = "deleted"
	if _, err = s.SaveMemory(t.Context(), saved, saved.Version); err != nil {
		t.Fatal("stale source blocked immediate forgetting", err)
	}
	if _, err = s.ReadEvent(t.Context(), []string{m.Scope}, ev.ID); err == nil {
		t.Fatal("revised forgotten source remained visible")
	}
}

func TestRelationRejectsDetachedOrQuotedKinship(t *testing.T) {
	cases := []struct{ text, predicate, word, quote string }{
		{"我问了你爸", "father", "问了你爸", "我问了你爸"},
		{"我问了你爸", "father", "爸", "我问了你爸"},
		{"我和她妈聊天", "mother", "和她妈", "我和她妈聊天"},
		{"我和她妈聊天", "mother", "妈", "我和她妈聊天"},
		{"兰说：“我爸回来了”", "father", "爸", "我爸回来了"},
		{"兰说：‘我爸回来了’", "father", "爸", "我爸回来了"},
		{"兰说：\"我爸回来了\"", "father", "爸", "我爸回来了"},
	}
	for _, tc := range cases {
		t.Run(tc.text+tc.word, func(t *testing.T) {
			s, c, in := memoryFixture(t)
			in.Text = tc.text
			if err := s.Observe(in, c, time.Now()); err != nil {
				t.Fatal(err)
			}
			scope := ScopeID(in.Channel, in.Account, in.Thread)
			page, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{scope}, Originals: true})
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			ev := page.Items[0].Event
			assertion := MemoryAssertion{Subject: in.Sender, Predicate: tc.predicate, Object: "relative:" + in.Sender + ":" + tc.predicate, AssertedBy: in.Sender, State: "explicit", SubjectBasis: "speaker", SubjectText: "我", ObjectBasis: "relative", ObjectText: tc.word, PredicateText: tc.word, Sources: []AssertionSource{{ev.ID, ev.Revision, tc.quote}}}
			_, err = s.SaveMemory(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "fact", Detail: "wrong kinship", Sources: []MemorySource{{ev.ID, tc.quote}}, Assertions: []MemoryAssertion{assertion}}, 0)
			if err == nil {
				t.Fatal("detached or reported possessor accepted as speaker's relative")
			}
		})
	}
}

func TestAdminCorrectionFailureLeavesNoSourceOrSummaryChange(t *testing.T) {
	for _, invalid := range []string{"subject", "predicate"} {
		t.Run(invalid, func(t *testing.T) {
			s, m, ev := relationFixture(t, false)
			if err := s.scopeTransaction(m.Scope, func(data *persisted) error {
				scope := data.Scopes[m.Scope]
				scope.Summary = "unchanged-summary"
				scope.SummaryBoundary = 1
				data.Scopes[m.Scope] = scope
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before := copyScope(s.data.Scopes[m.Scope])
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
				t.Fatal(err)
			}
			a := MemoryAssertion{Subject: "bob", Predicate: "father", Object: "relative:bob:father", SubjectBasis: "speaker", SubjectText: "我", ObjectBasis: "relative", ObjectText: "爸", PredicateText: "爸"}
			if invalid == "subject" {
				a.Subject = "other"
			} else {
				a.PredicateText = "问了你爸"
				a.ObjectText = a.PredicateText
			}
			_, err := s.SaveSourcedAdminCorrection(t.Context(), m.Scope, "bob", ev.ID, ev.Revision, "兰的这句是对我说的，你爸指我的爸爸", []MemoryAssertion{a})
			if err == nil {
				t.Fatal("invalid correction accepted")
			}
			var afterCount int
			if err = s.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&afterCount); err != nil {
				t.Fatal(err)
			}
			if count != afterCount || !reflect.DeepEqual(before, s.data.Scopes[m.Scope]) {
				t.Fatal("failed correction changed source count or scope")
			}
			page, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{m.Scope}, Query: "爸爸", Originals: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, hit := range page.Items {
				if hit.Event != nil && hit.Event.Role == "admin_correction" {
					t.Fatal("failed correction leaked to recall")
				}
			}
		})
	}
}

func TestRelationNegationTargetsPredicateNotWholeQuote(t *testing.T) {
	cases := []struct {
		text, kind     string
		negated, valid bool
	}{
		{"我爸不是医生", "father", false, true},
		{"我爸不是医生", "father", true, false},
		{"我爸并非医生", "father", false, true},
		{"我爸并非医生", "father", true, false},
		{"我爸不是医生", "occupation", true, true},
		{"我爸不是医生", "occupation", false, false},
		{"我爸是医生，我不是老师", "occupation", false, true},
		{"我爸是医生，我不是老师", "occupation", true, false},
		{"我爸不是老师，医生是别人", "occupation", true, false},
		{"我爸不是医生，我爸是医生", "occupation", true, false},
		{"我爸不是医生，我爸是医生", "occupation", false, false},
		{"我爸原来不是，现在是医生", "occupation", true, false},
		{"我爸原来不是，现在是医生", "occupation", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.text+tc.kind+fmt.Sprint(tc.negated), func(t *testing.T) {
			s, c, in := memoryFixture(t)
			in.Text = tc.text
			if err := s.Observe(in, c, time.Now()); err != nil {
				t.Fatal(err)
			}
			scope := ScopeID(in.Channel, in.Account, in.Thread)
			page, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{scope}, Originals: true})
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			ev := page.Items[0].Event
			a := MemoryAssertion{Subject: in.Sender, Predicate: "father", Object: "relative:" + in.Sender + ":father", AssertedBy: in.Sender, State: "explicit", SubjectBasis: "speaker", SubjectText: "我", ObjectBasis: "relative", ObjectText: "爸", PredicateText: "爸", Negated: tc.negated, Sources: []AssertionSource{{ev.ID, ev.Revision, tc.text}}}
			if tc.kind == "occupation" {
				a.Subject = a.Object
				a.SubjectBasis = "relative"
				a.SubjectText = "我爸"
				a.Predicate = "attribute"
				a.PredicateText = "是"
				a.Object = "医生"
				a.ObjectText = "医生"
				a.ObjectBasis = "literal"
				a.State = "reported"
			}
			_, err = s.SaveMemory(t.Context(), MemoryEntry{Scope: scope, Subject: a.Subject, Kind: "fact", Detail: tc.text, Sources: []MemorySource{{ev.ID, tc.text}}, Assertions: []MemoryAssertion{a}}, 0)
			if (err == nil) != tc.valid {
				t.Fatal("predicate polarity mismatch", tc, err)
			}
		})
	}
}
