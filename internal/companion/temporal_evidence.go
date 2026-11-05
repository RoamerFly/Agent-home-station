package companion

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// These are lexical time expressions, not extracted assertions. Resolving an
// explicitly relative expression does not certify the sentence containing it.
type temporalExpression struct {
	Text        string  `json:"text"`
	Kind        string  `json:"kind"`
	Precision   string  `json:"precision"`
	Date        string  `json:"resolved_date,omitempty"`
	MonthDay    string  `json:"month_day,omitempty"`
	Weekday     int     `json:"weekday_monday_1,omitempty"`
	WeekStart   string  `json:"calendar_week_start,omitempty"`
	Quantity    float64 `json:"quantity,omitempty"`
	Unit        string  `json:"unit,omitempty"`
	Seconds     float64 `json:"fixed_duration_seconds,omitempty"`
	Clock       string  `json:"clock,omitempty"`
	Anchor      string  `json:"resolution_anchor,omitempty"`
	Approximate bool    `json:"approximate_or_range,omitempty"`
}

type temporalSourcePrecision struct {
	EventID       string               `json:"event_id,omitempty"`
	MessageID     string               `json:"message_id,omitempty"`
	SpeakerID     string               `json:"speaker_id,omitempty"`
	SourceVersion int                  `json:"source_revision,omitempty"`
	UserTextState string               `json:"user_text_state"`
	SaidAt        string               `json:"utterance_local_time,omitempty"`
	Mentions      []temporalExpression `json:"expressions"`
	Ambiguous     bool                 `json:"sentence_requires_semantic_review"`
}

var (
	temporalFullDate        = regexp.MustCompile(`(?:[12][0-9]{3}年[0-9]{1,2}月[0-9]{1,2}[日号]|[12][0-9]{3}-[0-9]{1,2}-[0-9]{1,2})`)
	temporalDateParts       = regexp.MustCompile(`[0-9]+`)
	temporalMonthDay        = regexp.MustCompile(`[0-9]{1,2}月[0-9]{1,2}[日号]`)
	temporalWeekday         = regexp.MustCompile(`(上上|下下|本|这|上|下)?个?(星期|礼拜|周)([一二三四五六日天1-7末])`)
	temporalWeekPeriod      = regexp.MustCompile(`(上上|下下|本|这|上|下)个?(星期|礼拜|周)`)
	temporalRelativeDay     = regexp.MustCompile(`大前天|大后天|前天|昨天|今天|明天|后天`)
	temporalQuantity        = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?|[零〇一二两三四五六七八九十百千万半]+)(秒钟|分钟|个小时|小时|个月|秒|分|天|日|周|月|年)(之前|之后|以前|以后|前|后|以内|之内|内)?`)
	temporalClock           = regexp.MustCompile(`(?:[01]?[0-9]|2[0-3]):[0-5][0-9](?::[0-5][0-9])?`)
	temporalSourceUncertain = regexp.MustCompile(`[?？“”「」"]|吗|是不是|是否|假如|如果|不|没|未|别|还是|或者|或许|可能`)
)

func temporalNumber(text string) (float64, bool) {
	if text == "半" {
		return .5, true
	}
	if n, err := strconv.ParseFloat(text, 64); err == nil {
		return n, !math.IsInf(n, 0) && !math.IsNaN(n)
	}
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	units := map[rune]int{'十': 10, '百': 100, '千': 1000, '万': 10000}
	if !strings.ContainsAny(text, "十百千万") && len([]rune(text)) > 1 {
		return 0, false // e.g. 一二 is not an exact quantity of two.
	}
	total, section, digit := float64(0), float64(0), float64(0)
	for _, r := range text {
		if n, ok := digits[r]; ok {
			digit = float64(n)
		} else if unit, ok := units[r]; ok {
			if unit == 10000 {
				section += digit
				if section == 0 {
					section = 1
				}
				total += section * 10000
				section, digit = 0, 0
				continue
			}
			if digit == 0 {
				digit = 1
			}
			section += digit * float64(unit)
			digit = 0
		} else {
			return 0, false
		}
	}
	value := total + section + digit
	return value, text != "" && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func temporalWeekdayNumber(s string) int {
	return map[string]int{"一": 1, "二": 2, "三": 3, "四": 4, "五": 5, "六": 6, "日": 7, "天": 7, "1": 1, "2": 2, "3": 3, "4": 4, "5": 5, "6": 6, "7": 7}[s]
}

func temporalDate(y, m, d int, loc *time.Location) string {
	at := time.Date(y, time.Month(m), d, 0, 0, 0, 0, loc)
	if at.Year() != y || int(at.Month()) != m || at.Day() != d {
		return ""
	}
	return at.Format("2006-01-02")
}

func temporalExpressions(text string, saidAt time.Time, timezone string) []temporalExpression {
	loc := location(timezone)
	anchor := saidAt.In(loc)
	out := []temporalExpression{}
	covered := [][2]int{}
	for _, span := range temporalFullDate.FindAllStringIndex(text, -1) {
		literal := text[span[0]:span[1]]
		nums := temporalDateParts.FindAllString(literal, -1)
		y, _ := strconv.Atoi(nums[0])
		m, _ := strconv.Atoi(nums[1])
		d, _ := strconv.Atoi(nums[2])
		date := temporalDate(y, m, d, loc)
		if date != "" {
			out = append(out, temporalExpression{Text: literal, Kind: "calendar_date", Precision: "year_month_day", Date: date, MonthDay: date[5:], Anchor: "explicit_expression"})
		} else {
			out = append(out, temporalExpression{Text: literal, Kind: "calendar_date", Precision: "invalid_calendar_date", Anchor: "invalid_expression"})
		}
		covered = append(covered, [2]int{span[0], span[1]})
	}
	for _, span := range temporalMonthDay.FindAllStringIndex(text, -1) {
		inside := false
		for _, bound := range covered {
			inside = inside || span[0] >= bound[0] && span[1] <= bound[1]
		}
		if inside {
			continue
		}
		literal := text[span[0]:span[1]]
		nums := temporalDateParts.FindAllString(literal, -1)
		m, _ := strconv.Atoi(nums[0])
		d, _ := strconv.Atoi(nums[1])
		// Leap year is used only to validate month/day, never to supply a year.
		date := temporalDate(2000, m, d, loc)
		if date != "" {
			out = append(out, temporalExpression{Text: literal, Kind: "calendar_date", Precision: "month_day_year_unknown", MonthDay: date[5:], Anchor: "year_unknown"})
			covered = append(covered, [2]int{span[0], span[1]})
		}
	}
	for _, match := range temporalWeekday.FindAllStringSubmatch(text, -1) {
		m := temporalExpression{Text: match[0], Kind: "weekday", Precision: "weekday_week_unknown", Weekday: temporalWeekdayNumber(match[3]), Anchor: "week_unknown"}
		if match[1] != "" {
			m.Kind, m.Precision, m.Anchor = "qualified_weekday", "calendar_week_and_weekday", "utterance_time_explicit_relative_week"
			if !saidAt.IsZero() && m.Weekday > 0 {
				weekday := int(anchor.Weekday())
				if weekday == 0 {
					weekday = 7
				}
				offset := map[string]int{"本": 0, "这": 0, "上": -1, "下": 1, "上上": -2, "下下": 2}[match[1]]
				m.Date = anchor.AddDate(0, 0, offset*7+m.Weekday-weekday).Format("2006-01-02")
			}
		}
		if match[3] == "末" {
			m.Precision, m.Date = "weekend_day_unspecified", ""
		}
		out = append(out, m)
	}
	for _, match := range temporalWeekPeriod.FindAllStringSubmatch(text, -1) {
		// Preserve period precision separately: it cannot certify an event day.
		m := temporalExpression{Text: match[0], Kind: "calendar_week", Precision: "calendar_week_day_unknown", Anchor: "utterance_time_explicit_relative_week"}
		if !saidAt.IsZero() {
			weekday := int(anchor.Weekday())
			if weekday == 0 {
				weekday = 7
			}
			offset := map[string]int{"本": 0, "这": 0, "上": -1, "下": 1, "上上": -2, "下下": 2}[match[1]]
			m.WeekStart = anchor.AddDate(0, 0, offset*7+1-weekday).Format("2006-01-02")
		}
		out = append(out, m)
	}
	for _, literal := range temporalRelativeDay.FindAllString(text, -1) {
		m := temporalExpression{Text: literal, Kind: "relative_day", Precision: "relative_calendar_day", Anchor: "utterance_time_explicit_relative_day"}
		if !saidAt.IsZero() {
			offset := map[string]int{"大前天": -3, "前天": -2, "昨天": -1, "今天": 0, "明天": 1, "后天": 2, "大后天": 3}[literal]
			m.Date = anchor.AddDate(0, 0, offset).Format("2006-01-02")
		}
		out = append(out, m)
	}
	for _, span := range temporalQuantity.FindAllStringSubmatchIndex(text, -1) {
		inside := false
		for _, bound := range covered {
			inside = inside || span[0] >= bound[0] && span[0] < bound[1]
		}
		if inside || strings.HasSuffix(text[:span[0]], "点") || strings.HasSuffix(text[:span[0]], ":") {
			continue
		}
		literal := text[span[0]:span[1]]
		quantity, ok := temporalNumber(text[span[2]:span[3]])
		if !ok {
			continue
		}
		originalUnit := text[span[4]:span[5]]
		unit := map[string]string{"秒钟": "second", "秒": "second", "分钟": "minute", "分": "minute", "个小时": "hour", "小时": "hour", "天": "day", "日": "day", "周": "week", "个月": "month", "月": "month", "年": "year"}[originalUnit]
		m := temporalExpression{Text: literal, Kind: "duration", Precision: "quantified_duration", Quantity: quantity, Unit: unit, Anchor: "event_duration_not_message_age"}
		if scale := map[string]float64{"second": 1, "minute": 60, "hour": 3600}[unit]; scale > 0 {
			m.Seconds = quantity * scale
		}
		if span[6] >= 0 {
			m.Kind, m.Anchor = "relative_interval", "explicit_interval_requires_event_anchor"
		} else if originalUnit == "年" && quantity >= 1000 && quantity <= 2999 {
			m.Kind, m.Precision, m.Anchor = "calendar_year", "year_only", "calendar_year_not_duration"
		} else if originalUnit == "月" && quantity >= 1 && quantity <= 12 {
			m.Kind, m.Precision, m.Anchor = "calendar_month", "month_year_unknown", "calendar_month_not_duration"
		} else if originalUnit == "日" && quantity >= 1 && quantity <= 31 {
			m.Kind, m.Precision, m.Anchor = "calendar_day", "day_month_unknown", "calendar_day_not_duration"
		}
		prefix := text[max(0, span[0]-12):span[0]]
		suffix := text[span[1]:min(len(text), span[1]+9)]
		for _, qualifier := range []string{"约", "大概", "差不多", "至少", "最多", "到", "至", "~", "～", "-", "左右", "上下", "以上", "以下"} {
			m.Approximate = m.Approximate || strings.Contains(prefix, qualifier) || strings.HasPrefix(suffix, qualifier)
		}
		out = append(out, m)
	}
	for _, literal := range temporalClock.FindAllString(text, -1) {
		out = append(out, temporalExpression{Text: literal, Kind: "clock", Precision: "clock_time_date_unknown", Clock: literal, Anchor: "event_date_unknown"})
	}
	return out
}

func temporalInputTime(in Input) time.Time {
	stamp := in.MessageTime
	if in.Source != nil && in.Source.OccurredAt != "" {
		stamp = in.Source.OccurredAt
	} else if runtimeInput(in) {
		// A resumed task's wake time cannot anchor the original user's 明天.
		return time.Time{}
	}
	at, _ := time.Parse(time.RFC3339Nano, stamp)
	return at
}

func temporalLiteralPrecision(literal, state string, saidAt time.Time, timezone string) map[string]any {
	if !reliableLiteral(literal, state) && state != "referenced_original" {
		return nil
	}
	expressions := temporalExpressions(literal, saidAt, timezone)
	if len(expressions) == 0 {
		return nil
	}
	return map[string]any{"evidence_kind": "lexical_time_precision_not_extracted_facts", "message_time_is_event_time": false, "bare_weekday_has_known_week": false, "expressions": expressions}
}

func (e *Engine) temporalSources(ctx context.Context, in Input) []temporalSourcePrecision {
	out := []temporalSourcePrecision{}
	add := func(eventID, messageID, sender, state, literal string, revision int, at time.Time) {
		if sender == "" || !reliableLiteral(literal, state) && state != "referenced_original" {
			return
		}
		mentions := temporalExpressions(literal, at, e.Config.Timezone)
		if len(mentions) == 0 {
			return
		}
		row := temporalSourcePrecision{EventID: eventID, MessageID: messageID, SpeakerID: sender, SourceVersion: revision, UserTextState: state, Mentions: mentions, Ambiguous: temporalSourceUncertain.MatchString(literal)}
		if !at.IsZero() {
			row.SaidAt = at.In(location(e.Config.Timezone)).Format(time.RFC3339Nano)
		}
		out = append(out, row)
	}
	literal, state := e.inputLiteral(in)
	at := temporalInputTime(in)
	id, revision := "", 0
	if in.Source != nil {
		id, revision = in.Source.EventID, in.Source.Revision
	}
	add(id, in.TraceID, in.Sender, state, literal, revision, at)
	scope := e.groundingScope(in)
	selected := map[string]bool{}
	for _, packet := range e.contextTurns(scope, in) {
		var envelope struct {
			EventID  string `json:"source_event_id"`
			Timeline []struct {
				EventID string `json:"source_event_id"`
			} `json:"timeline"`
		}
		if json.Unmarshal([]byte(packet["content"]), &envelope) == nil {
			selected[envelope.EventID] = envelope.EventID != ""
			for _, row := range envelope.Timeline {
				selected[row.EventID] = row.EventID != ""
			}
		}
	}
	for _, original := range groundedHistory(scope, in) {
		if original.Role != "user" || !selected[original.EventID] || original.EventID != "" && original.EventID == id || original.TraceID != "" && original.TraceID == in.TraceID {
			continue
		}
		turn := e.evidenceTurn(ctx, scope.ID, original)
		literal, state := literalTurn(turn)
		revision := 0
		if turn.Source != nil {
			revision = turn.Source.Revision
		}
		add(turn.EventID, turn.TraceID, turn.Sender, state, literal, revision, turn.Time)
	}
	return out
}

func temporalExpressionSupported(candidate, source temporalExpression) bool {
	if candidate.Kind == "relative_interval" || candidate.Kind == "clock" || candidate.Precision == "invalid_calendar_date" {
		// Identical relative words can move their event time when repeated
		// later. Clock expressions alone similarly lack an event-date binding.
		return false
	}
	if source.Approximate && !candidate.Approximate {
		return false
	}
	if candidate.Date != "" {
		return source.Date != "" && candidate.Date == source.Date
	}
	if candidate.Kind == "calendar_week" && candidate.WeekStart != "" {
		if candidate.WeekStart == source.WeekStart {
			return true
		}
		if source.Date != "" {
			date, err := time.Parse("2006-01-02", source.Date)
			if err == nil {
				day := int(date.Weekday())
				if day == 0 {
					day = 7
				}
				return candidate.WeekStart == date.AddDate(0, 0, 1-day).Format("2006-01-02")
			}
		}
	}
	if candidate.Precision == "weekday_week_unknown" {
		if candidate.Weekday > 0 && candidate.Weekday == source.Weekday {
			return true
		}
		if source.Date != "" {
			date, err := time.Parse("2006-01-02", source.Date)
			if err == nil {
				day := int(date.Weekday())
				if day == 0 {
					day = 7
				}
				return day == candidate.Weekday
			}
		}
	}
	if candidate.MonthDay != "" && candidate.Date == "" {
		return candidate.MonthDay == source.MonthDay || source.Date != "" && candidate.MonthDay == source.Date[5:]
	}
	if candidate.Kind == "duration" && source.Kind == candidate.Kind {
		return candidate.Unit == source.Unit && candidate.Quantity == source.Quantity || candidate.Seconds > 0 && candidate.Seconds == source.Seconds
	}
	if candidate.Kind == "clock" && source.Kind == "clock" {
		return candidate.Clock == source.Clock
	}
	return candidate.Kind == source.Kind && candidate.Text == source.Text
}

func (e *Engine) temporalSpecificityRisk(ctx context.Context, in Input, d Decision) bool {
	now := time.Now().In(location(e.Config.Timezone))
	byMessage := make([][]temporalExpression, len(d.Messages))
	mentioned := false
	for i, message := range d.Messages {
		byMessage[i] = temporalExpressions(message, now, e.Config.Timezone)
		mentioned = mentioned || len(byMessage[i]) > 0
	}
	if !mentioned {
		return false // Ordinary greetings do not read extra history or call a reviewer.
	}
	sources := e.temporalSources(ctx, in)
	for i, expressions := range byMessage {
		targets := []string{in.Sender}
		if i < len(d.ReplyContexts) && len(d.ReplyContexts[i].TargetIDs) > 0 {
			targets = d.ReplyContexts[i].TargetIDs
		}
		for _, candidate := range expressions {
			supported := false
			for _, source := range sources {
				if source.Ambiguous {
					continue
				}
				person := false
				for _, target := range targets {
					person = person || source.SpeakerID == target
				}
				if !person {
					continue
				}
				for _, expression := range source.Mentions {
					supported = supported || temporalExpressionSupported(candidate, expression)
				}
			}
			if !supported {
				return true
			}
		}
	}
	return false
}

func (e *Engine) temporalReviewEvidence(ctx context.Context, in Input, d Decision, now time.Time) map[string]any {
	candidates := []map[string]any{}
	for i, message := range d.Messages {
		if expressions := temporalExpressions(message, now, e.Config.Timezone); len(expressions) > 0 {
			candidates = append(candidates, map[string]any{"message_index": i, "expressions": expressions})
		}
	}
	return map[string]any{
		"evidence_kind":               "temporal_expression_precision_not_extracted_facts",
		"message_time_is_event_time":  false,
		"bare_weekday_has_known_week": false,
		"interpretation":              "Expressions are lexical hints, not proof of an event or its participants. A weekday alone does not choose this/next week. Resolve only explicit relative words against that source's utterance time in the configured timezone; never resolve an unqualified weekday from message time. Month/day alone does not supply a year. Check question, negation, quotation and event scope in the original sources before allowing a more specific date, interval or duration.",
		"sources":                     e.temporalSources(ctx, in),
		"candidates":                  candidates,
	}
}
