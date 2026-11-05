package companion

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func temporalExpressionOf(t *testing.T, text, kind string, at time.Time, timezone string) temporalExpression {
	t.Helper()
	for _, expression := range temporalExpressions(text, at, timezone) {
		if expression.Kind == kind {
			return expression
		}
	}
	t.Fatalf("missing expression %s in %q", kind, text)
	return temporalExpression{}
}

func TestTemporalPrecisionDoesNotAssignWeekOrYearFromMessageDate(t *testing.T) {
	at, _ := time.Parse(time.RFC3339, "2026-10-30T20:51:00Z")
	for _, weekday := range []string{"星期六", "周六", "礼拜六"} {
		for _, timezone := range []string{"Asia/Shanghai", "UTC", "America/Los_Angeles"} {
			m := temporalExpressionOf(t, "我的下次计划是"+weekday+"去苏州。", "weekday", at, timezone)
			if m.Date != "" || m.WeekStart != "" || m.Weekday != 6 || m.Precision != "weekday_week_unknown" || m.Anchor != "week_unknown" {
				t.Fatal("weekday acquired a week from message occurrence", timezone, m)
			}
		}
	}
	partial := temporalExpressionOf(t, "计划11月7日出行。", "calendar_date", at, "Asia/Shanghai")
	if partial.Date != "" || partial.MonthDay != "11-07" || partial.Precision != "month_day_year_unknown" {
		t.Fatal("partial date acquired current year", partial)
	}
	if precision := temporalLiteralPrecision("我准备出行", "verified_original", at, "Asia/Shanghai"); precision != nil {
		t.Fatal("message occurrence alone created a life-event date", precision)
	}
}

func TestTemporalExplicitRelativeResolutionUsesOriginalUtteranceTimezone(t *testing.T) {
	for _, tc := range []struct {
		text, stamp, timezone, kind, date string
	}{
		{"下周一", "2026-11-01T16:30:00Z", "Asia/Shanghai", "qualified_weekday", "2026-11-09"},
		{"下周一", "2026-11-01T16:30:00Z", "UTC", "qualified_weekday", "2026-11-02"},
		{"明天", "2026-10-31T23:30:00Z", "Asia/Shanghai", "relative_day", "2026-11-02"},
		{"明天", "2026-10-31T23:30:00Z", "UTC", "relative_day", "2026-11-01"},
		{"2026年11月7日", "2026-10-31T23:30:00Z", "Asia/Shanghai", "calendar_date", "2026-11-07"},
		{"2026-11-07", "2026-10-31T23:30:00Z", "UTC", "calendar_date", "2026-11-07"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.stamp)
		m := temporalExpressionOf(t, tc.text, tc.kind, at, tc.timezone)
		if m.Date != tc.date {
			t.Fatal(tc, m)
		}
	}
	for _, phrase := range []string{"下周六", "明天"} {
		for _, m := range temporalExpressions(phrase, time.Time{}, "Asia/Shanghai") {
			if m.Date != "" || m.WeekStart != "" {
				t.Fatal("missing utterance time was replaced with now", m)
			}
		}
	}
	original := "2026-10-30T20:51:00Z"
	in := Input{BackgroundResult: true, MessageTime: "2026-11-10T10:00:00Z", Source: &SourceRef{OccurredAt: original}}
	if got := temporalInputTime(in).Format(time.RFC3339); got != original {
		t.Fatal("task wake time changed original relative-time anchor", got)
	}
	in.Source = nil
	if !temporalInputTime(in).IsZero() {
		t.Fatal("task wrapper without source acquired a human utterance time")
	}
}

func TestTemporalPrecisionSupportsEntailedDatesWithoutHardReject(t *testing.T) {
	at, _ := time.Parse(time.RFC3339, "2026-10-30T20:51:00Z")
	for _, tc := range []struct {
		source, sourceKind, draft, draftKind string
		supported                            bool
	}{
		{"星期六", "weekday", "下周六", "qualified_weekday", false},
		{"星期六", "weekday", "本周六", "qualified_weekday", false},
		{"星期六", "weekday", "周六", "weekday", true},
		{"2026年11月7日", "calendar_date", "下周六", "qualified_weekday", true},
		{"明天", "relative_day", "2026-11-01", "calendar_date", true},
		{"11月7日", "calendar_date", "2026-11-07", "calendar_date", false},
		{"三分钟", "duration", "180秒", "duration", true},
		{"约三分钟", "duration", "三分钟", "duration", false},
		{"三天后", "relative_interval", "三天后", "relative_interval", false},
		{"三天", "duration", "三小时", "duration", false},
		{"下周", "calendar_week", "下周六", "qualified_weekday", false},
		{"2026年2月30日", "calendar_date", "2026年2月30日", "calendar_date", false},
		{"半个小时", "duration", "30分钟", "duration", true},
	} {
		source := temporalExpressionOf(t, tc.source, tc.sourceKind, at, "Asia/Shanghai")
		draft := temporalExpressionOf(t, tc.draft, tc.draftKind, at, "Asia/Shanghai")
		if got := temporalExpressionSupported(draft, source); got != tc.supported {
			t.Fatal(tc, source, draft, got)
		}
	}
	for _, m := range temporalExpressions("2026年11月7日", at, "Asia/Shanghai") {
		if m.Kind == "duration" {
			t.Fatal("calendar components became event durations", m)
		}
	}
	for _, text := range []string{"2026年11月7日后", "十五点三十分", "15:30分"} {
		for _, m := range temporalExpressions(text, at, "Asia/Shanghai") {
			if m.Kind == "duration" || m.Kind == "relative_interval" {
				t.Fatal("calendar/clock component became an event duration", text, m)
			}
		}
	}
}

func temporalGroundingFixture(t *testing.T, sourceText string) (*Engine, Input, *scopedReviewProtocolModel) {
	t.Helper()
	s, c, _ := memoryFixture(t)
	c.Timezone = "Asia/Shanghai"
	_ = groundedInput(t, s, c, "alpha", "trip-source", sourceText)
	in := groundedInput(t, s, c, "alpha", "trip-question", "恢复以后，你还记得我的下次出行计划是什么吗？")
	model := &scopedReviewProtocolModel{verdict: "allow"}
	return &Engine{Store: s, Config: c, model: model}, in, model
}

func TestTemporalSpecificityRoutesKnownFailureIntoOneReadOnlyReview(t *testing.T) {
	e, in, model := temporalGroundingFixture(t, "我的下次计划是星期六去苏州。")
	draft := Decision{Action: "reply", Messages: []string{"记得呀，你说下周六去苏州。"}}
	before := recallEpochForTest(t, e.Store)
	if !e.temporalSpecificityRisk(t.Context(), in, draft) {
		t.Fatal("a weekday became a selected calendar week on the fast path")
	}
	checked, err := e.DiagnoseGroundedReply(t.Context(), in, draft)
	if err != nil || checked.Action != "reply" || strings.Join(checked.Messages, "") != strings.Join(draft.Messages, "") || model.state == nil {
		t.Fatal("temporal routing edited/rejected a draft independently of the reviewer", checked, err)
	}
	var precision map[string]any
	rows := atomicTypedRows(model.state, "temporal_contract")
	if len(rows) != 1 || json.Unmarshal([]byte(rows[0].Text), &precision) != nil || precision["message_time_is_event_time"] != false || precision["bare_weekday_has_known_week"] != false {
		t.Fatal("readonly reviewer omitted explicit unknown precision", precision)
	}
	encoded, _ := json.Marshal(precision)
	if !strings.Contains(string(encoded), "weekday_week_unknown") || !strings.Contains(string(encoded), "calendar_week_and_weekday") {
		t.Fatal("source and draft precision were not separately visible", string(encoded))
	}
	if after := recallEpochForTest(t, e.Store); after != before {
		t.Fatal("time metadata changed persistent source/evidence epoch", before, after)
	}
}

func TestTemporalBareWeekdayAndExplicitSupportedWeekRemainUsable(t *testing.T) {
	for _, tc := range []struct{ source, draft string }{
		{"我的下次计划是星期六去苏州。", "你星期六去苏州。"},
		{"我的下次计划是2026年11月7日去苏州。", "你2026年11月7日去苏州。"},
	} {
		e, in, _ := temporalGroundingFixture(t, tc.source)
		if e.temporalSpecificityRisk(t.Context(), in, Decision{Messages: []string{tc.draft}}) {
			t.Fatal("unchanged supported precision was labeled a new claim", tc)
		}
	}
	for _, source := range []string{"你说是下周六吗？", "我下周六不去苏州。", "如果下周六去苏州，我再告诉你。"} {
		e, in, _ := temporalGroundingFixture(t, source)
		if !e.temporalSpecificityRisk(t.Context(), in, Decision{Messages: []string{"你下周六去苏州。"}}) {
			t.Fatal("time token in question/negation/condition became positive event proof", source)
		}
	}
}

func TestTemporalOrdinaryGreetingDoesNotReadHistoryOrRequireModel(t *testing.T) {
	// A Store is deliberately absent: the no-time fast path must not read it.
	e := &Engine{Config: Defaults()}
	if e.temporalSpecificityRisk(t.Context(), Input{}, Decision{Messages: []string{"晚安，好梦～"}}) {
		t.Fatal("ordinary greeting unexpectedly entered time review")
	}
}

func TestTemporalInitialPayloadLabelsPrecisionButNotObservationOrAssistantAsFact(t *testing.T) {
	e, in, _ := temporalGroundingFixture(t, "我的下次计划是星期六去苏州。")
	scope := e.groundingScope(in)
	var source Turn
	for _, turn := range scope.Messages {
		if turn.TraceID == "trip-source" {
			source = turn
		}
	}
	row := e.turnEvidenceFields(scope.ID, source, 1200)
	if row["text"] != "我的下次计划是星期六去苏州。" || row["speaker_id"] != "alpha" || row["temporal_precision"] == nil {
		t.Fatal("source text/actor changed or initial precision missing", row)
	}
	empty := ""
	in.OriginalText = &empty
	in.Text = "机器观察声称：下周六去苏州"
	in.IncomingMedia = []IncomingMediaRef{{Kind: "voice", UploaderID: in.Sender, Observation: in.Text, ObservationState: "observed"}}
	if row := e.inputEvidenceFields(in); row["temporal_precision"] != nil || row["text"] != "" || row["media_observations"] == nil {
		t.Fatal("machine observation became literal time evidence", row)
	}
	for _, state := range []string{"unknown", "assistant_statement", "unverified_reference"} {
		if temporalLiteralPrecision("下周六去苏州", state, time.Now(), e.Config.Timezone) != nil {
			t.Fatal("non-user or unreliable statement received human time precision", state)
		}
	}
}
