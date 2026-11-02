package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

type summarySourceRepairModel struct {
	id            string
	calls         int
	alwaysInvalid bool
}

func (m *summarySourceRepairModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	id := m.id
	if m.calls == 1 || m.alwaysInvalid {
		id = "invented-unseen-source"
	}
	s := contextSummary{Version: 1, People: []summaryPerson{}, Topics: []summaryTopic{{Text: "合成整理任务", State: "active", SourceEventIDs: []string{id}}}, Media: []summaryMedia{}, PendingTasks: []summaryTask{}, Preferences: []string{}, Corrections: []string{}}
	b, _ := json.Marshal(s)
	return string(b), nil
}

func TestCompressionRepairsSourceIDsOnceAndPreservesOriginalOnFailure(t *testing.T) {
	for _, bad := range []bool{false, true} {
		c := enabledConfig()
		c.Permissions.Remember = true
		c.Context = &ContextSettings{true, 20, 1, 4000}
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		in := Input{Channel: "wechat", Account: "synthetic", Thread: "synthetic-group"}
		for i := 0; i < 3; i++ {
			in.TraceID = fmt.Sprint(i)
			in.Sender = "synthetic-member"
			in.Text = "合成整理任务"
			if err = s.Observe(in, c, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		scope, err := s.Read(ScopeID(in.Channel, in.Account, in.Thread), c, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		m := &summarySourceRepairModel{id: scope.Messages[0].EventID, alwaysInvalid: bad}
		e := &Engine{Config: c, Store: s, model: m}
		changed, err := e.CompressContext(t.Context(), in, true)
		if m.calls != 2 {
			t.Fatal("source repair unbounded or skipped", m.calls, err)
		}
		after, _ := s.Read(scope.ID, c, time.Now())
		if len(after.Messages) != 3 {
			t.Fatal("original messages removed")
		}
		if bad {
			if changed || !errors.Is(err, ErrSummarySourceInvalid) || after.Summary != "" || after.CompressedCount != 0 {
				t.Fatal(changed, err, after.CompressedCount)
			}
		} else {
			var summary contextSummary
			json.Unmarshal([]byte(after.Summary), &summary)
			if err != nil || !changed || len(summary.Sources) != 1 || summary.Sources[0].EventID != m.id {
				t.Fatal(changed, err, summary.Sources)
			}
		}
	}
}

func TestSummaryCannotGrantItselfUnseenSource(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "member", "one", "一个合成事实")
	other := groundedInput(t, s, c, "member", "other", "另一条不在摘要输入的事实")
	r, ok := s.SourceForInput(other)
	if !ok {
		t.Fatal("missing fixture source")
	}
	summary := contextSummary{Topics: []summaryTopic{{Text: "不能自行引入", SourceEventIDs: []string{r.EventID}}}, Sources: []SourceRef{*r}}
	scope, _ := s.Read(ScopeID(in.Channel, in.Account, in.Thread), c, time.Now())
	if err := (&Engine{Store: s, Config: c}).bindSummarySources(t.Context(), in, &summary, scope.Messages[:1], nil); !errors.Is(err, ErrSummarySourceInvalid) {
		t.Fatal("model sources array granted provenance", err)
	}
}
