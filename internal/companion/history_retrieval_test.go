package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/mcptools"
)

type historyTools struct {
	names    []string
	failRead bool
}

func (h *historyTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "memory_search", Description: "original directory", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "memory_read", Description: "read original", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}

type largeDiscoveryTools struct{}

func (largeDiscoveryTools) List(context.Context) ([]mcptools.Tool, error) {
	out := []mcptools.Tool{}
	for i := 0; i < 150; i++ {
		out = append(out, mcptools.Tool{Name: fmt.Sprintf("tool_%03d", i), Description: strings.Repeat("description ", 90) + "full_description_needle", InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return out, nil
}
func (largeDiscoveryTools) Call(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}

type largeDiscoveryModel struct {
	steps int
	found bool
}

func (*largeDiscoveryModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}
func (m *largeDiscoveryModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	m.steps++
	if m.steps == 1 {
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"tool_149"}}`, nil
	}
	for _, tool := range tools {
		if tool.Name == "tool_149" && strings.Contains(tool.Description, "full_description_needle") {
			m.found = true
		}
	}
	return `{"action":"reply","messages":["已找到对应能力的完整说明。"]}`, nil
}
func TestNativeDiscoveryDoesNotDropToolsBeyondLegacyCatalogCapacity(t *testing.T) {
	m := &largeDiscoveryModel{}
	e, err := NewPreview(enabledConfig(), m, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = largeDiscoveryTools{}
	e.MaxToolCalls = 2
	d, err := e.Respond(t.Context(), Input{Preview: true, Mode: "direct", Text: "查看已安装能力"})
	if err != nil || d.Action != "reply" || !m.found || m.steps != 2 {
		t.Fatal("native discovery silently discarded a registered capability", d, err, m.found, m.steps)
	}
}
func (h *historyTools) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	h.names = append(h.names, name)
	if name == "memory_search" {
		return `{"items":[{"event":{"id":"older-video-event","message_id":"older-video-native","observation_bytes":500}}],"coverage":"indexed_originals","has_more":true,"next_cursor":"actual-next"}`, nil
	}
	if h.failRead {
		return "", errors.New("synthetic observation temporarily unavailable")
	}
	return `{"part":"observation","text":"合成视频讲述如何整理书架。","has_more":false}`, nil
}

type historyModel struct {
	steps             int
	ignore            bool
	prepared          bool
	reportReadFailure bool
}

func (m *historyModel) Chat(_ context.Context, _ string, _ []map[string]string) (string, error) {
	return "", nil
}
func (m *historyModel) ChatFunctions(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
	m.steps++
	for _, turn := range turns {
		m.prepared = m.prepared || strings.Contains(turn["content"], "older-video-event")
	}
	if m.steps == 1 {
		return `{"action":"tool","tool":"memory_search","arguments":{"originals":true,"before_message_id":"anchor","media_kind":"video_or_share","order":"time_desc","metadata_only":true,"limit":8}}`, nil
	}
	if m.ignore {
		return `{"action":"reply","messages":["只看到了引用视频，请把另外几条重新发来。"]}`, nil
	}
	if m.steps == 2 {
		return `{"action":"tool","tool":"memory_read","arguments":{"id":"older-video-event","event":true,"part":"observation"}}`, nil
	}
	if m.reportReadFailure {
		return `{"action":"reply","messages":["已找到原消息，但保存的观察暂时读不到；已保留来源编号，可从这里继续检查。"]}`, nil
	}
	return `{"action":"reply","messages":["已读取保存的前序视频观察，当前内容是整理书架。"]}`, nil
}
func TestHistoryDirectoryIsModelSelectedAndPartialReplyIsNotBlocked(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		m := &historyModel{ignore: ignore}
		tools := &historyTools{}
		e, err := NewPreview(enabledConfig(), m, "synthetic persona")
		if err != nil {
			t.Fatal(err)
		}
		e.Tools = tools
		e.MaxToolCalls = 5
		d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "总结本群这条之前的前面8条视频", QuotedSource: &SourceRef{MessageID: "anchor"}})
		if err != nil || !m.prepared || len(tools.names) == 0 || tools.names[0] != "memory_search" {
			t.Fatal(d, err, tools.names)
		}
		if ignore {
			if d.Action != "reply" || d.Reason == "history_materials_not_inspected" || m.steps != 2 || len(tools.names) != 1 {
				t.Fatal("partial history reply was unnecessarily blocked", d, m.steps, tools.names)
			}
		} else {
			if d.Action != "reply" || m.steps != 3 || len(tools.names) != 2 || tools.names[1] != "memory_read" {
				t.Fatal(d, m.steps, tools.names)
			}
		}
	}
}

func TestHistoryRequestCanReplyWithoutAutomaticPrefetch(t *testing.T) {
	tools := &historyTools{}
	direct := &sequenceModel{answers: []string{`{"action":"reply","messages":["我先按这条消息能确认的内容整理。"]}`}}
	e, err := NewPreview(enabledConfig(), direct, "synthetic persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = tools
	e.MaxToolCalls = 5
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "总结本群这条之前的前面8条视频", QuotedSource: &SourceRef{MessageID: "anchor"}})
	if err != nil || d.Action != "reply" || len(tools.names) != 0 || len(direct.turns) != 1 {
		t.Fatal("host prefetch or unread interception returned", d, err, tools.names, len(direct.turns))
	}
}

type partialHistoryTools struct {
	names   []string
	failIDs map[string]bool
}

func (p *partialHistoryTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "memory_search", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "memory_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (p *partialHistoryTools) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	p.names = append(p.names, name)
	if name == "memory_search" {
		return `{"items":[{"event":{"id":"E8","message_id":"M8","at":"2026-11-01T08:00:00Z"}},{"event":{"id":"E7","message_id":"M7","at":"2026-11-01T07:00:00Z"}},{"event":{"id":"E6","message_id":"M6","at":"2026-11-01T06:00:00Z"}},{"event":{"id":"E5","message_id":"M5","at":"2026-11-01T05:00:00Z"}},{"event":{"id":"E4","message_id":"M4","at":"2026-11-01T04:00:00Z"}},{"event":{"id":"E3","message_id":"M3","at":"2026-11-01T03:00:00Z"}},{"event":{"id":"E2","message_id":"M2","at":"2026-11-01T02:00:00Z"}},{"event":{"id":"E1","message_id":"M1","at":"2026-11-01T01:00:00Z"}}],"has_more":false}`, nil
	}
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(args, &in)
	if p.failIDs[in.ID] {
		return "", errors.New("synthetic stored transcript unavailable")
	}
	return `{"event":{"id":"` + in.ID + `"},"text":"已读取 ` + in.ID + `","has_more":false}`, nil
}

type partialHistoryModel struct {
	step     int
	prepared string
}

func (*partialHistoryModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}
func (m *partialHistoryModel) ChatFunctions(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
	m.step++
	if m.step == 1 {
		return `{"action":"tool","tool":"memory_search","arguments":{"originals":true,"before_message_id":"anchor","media_kind":"video_or_share","order":"time_desc","metadata_only":true,"limit":8}}`, nil
	}
	if m.step >= 2 && m.step <= 7 {
		id := "E" + strconv.Itoa(m.step-1)
		return `{"action":"tool","tool":"memory_read","arguments":{"id":"` + id + `","event":true,"part":"transcript"}}`, nil
	}
	for _, turn := range turns {
		if strings.Contains(turn["content"], "chronological_history_materials") {
			m.prepared = turn["content"]
		}
	}
	return `{"action":"reply","messages":["已按当前能读取的六条视频整理，另外两条暂未读取。"]}`, nil
}

func TestEightVideoRequestMayFinishWithSixReadSources(t *testing.T) {
	model, tools := &partialHistoryModel{}, &partialHistoryTools{}
	e, err := NewPreview(enabledConfig(), model, "synthetic persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = tools, 8
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "总结本群这条之前的前面8条视频", QuotedSource: &SourceRef{MessageID: "anchor"}})
	if err != nil || d.Action != "reply" || len(tools.names) != 7 || strings.Count(model.prepared, `"read_verified":true`) != 6 || strings.Contains(model.prepared, "已读取 E7") || strings.Contains(model.prepared, "已读取 E8") {
		t.Fatal("partial historical result was not usable", d, err, tools.names, model.prepared)
	}
	if !(strings.Index(model.prepared, "已读取 E1") < strings.Index(model.prepared, "已读取 E2") && strings.Index(model.prepared, "已读取 E2") < strings.Index(model.prepared, "已读取 E6")) {
		t.Fatal("read sources were not presented chronologically", model.prepared)
	}
}

type failedHistoryModel struct{ step int }

func (*failedHistoryModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}
func (m *failedHistoryModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, _ []mcptools.Tool) (string, error) {
	m.step++
	if m.step == 1 {
		return `{"action":"tool","tool":"memory_search","arguments":{"originals":true,"before_message_id":"anchor","media_kind":"video_or_share","order":"time_desc","metadata_only":true,"limit":8}}`, nil
	}
	if m.step >= 2 && m.step <= 9 {
		id := "E" + strconv.Itoa(m.step-1)
		return `{"action":"tool","tool":"memory_read","arguments":{"id":"` + id + `","event":true,"part":"transcript"}}`, nil
	}
	return `{"action":"reply","messages":["六条已整理；另外两条读取失败，稍后可从原记录继续。"]}`, nil
}

func TestEightHistoryReadsSixSuccessTwoActualFailuresExposeCoverage(t *testing.T) {
	model := &failedHistoryModel{}
	tools := &partialHistoryTools{failIDs: map[string]bool{"E7": true, "E8": true}}
	e, err := NewPreview(enabledConfig(), model, "synthetic persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = tools, 10
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "总结本群这条之前的前面8条视频", QuotedSource: &SourceRef{MessageID: "anchor"}})
	if err != nil || d.Action != "reply" || d.HistoryCoverage == nil || d.HistoryCoverage.Requested != 8 || d.HistoryCoverage.Selected != 8 || d.HistoryCoverage.Attempted != 8 || d.HistoryCoverage.Read != 6 || d.HistoryCoverage.Failed != 2 || fmt.Sprint(d.HistoryCoverage.FailedEventIDs) != "[E7 E8]" || len(tools.names) != 9 {
		t.Fatal("actual partial history coverage was not retained", d, err, d.HistoryCoverage, tools.names)
	}
}

type failingReadTools struct {
	calls int
	ids   []string
}

func (*failingReadTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "memory_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (f *failingReadTools) Call(_ context.Context, _ string, args json.RawMessage) (string, error) {
	f.calls++
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(args, &in)
	f.ids = append(f.ids, in.ID)
	return "", &mcptools.WebError{Code: "network_timeout", RetryAfter: time.Millisecond}
}

type readRecoveryModel struct{ steps int }

func (*readRecoveryModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}
func (m *readRecoveryModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, _ []mcptools.Tool) (string, error) {
	m.steps++
	if m.steps == 1 || m.steps == 2 {
		return `{"action":"tool","tool":"memory_read","arguments":{"id":"A","event":true,"part":"transcript"}}`, nil
	}
	if m.steps == 3 {
		return `{"action":"tool","tool":"memory_read","arguments":{"id":"B","event":true,"part":"transcript"}}`, nil
	}
	return `{"action":"reply","messages":["这次暂时没读到，保留后续继续的位置。"]}`, nil
}

func TestReadOnlyFailuresAllowOnlyTwoRepairs(t *testing.T) {
	model, tools := &readRecoveryModel{}, &failingReadTools{}
	e, err := NewPreview(enabledConfig(), model, "synthetic persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = tools, 5
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "找一下之前的消息"})
	if err != nil || d.Action != "reply" || tools.calls != 6 || model.steps != 4 || fmt.Sprint(tools.ids) != "[A A A B B B]" {
		t.Fatal("read failure recovery was not bounded", d, err, tools.calls, model.steps)
	}
}

type parameterReadTools struct{ calls []string }

func (*parameterReadTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "memory_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (p *parameterReadTools) Call(_ context.Context, _ string, args json.RawMessage) (string, error) {
	var in struct {
		ID     string `json:"id"`
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(args, &in)
	p.calls = append(p.calls, in.ID+"/"+in.Cursor)
	return "", errors.New("参数 $.limit：无效")
}

type parameterRepairModel struct{ step int }

func (*parameterRepairModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}
func (m *parameterRepairModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, _ []mcptools.Tool) (string, error) {
	m.step++
	switch m.step {
	case 1, 2, 3, 4:
		return `{"action":"tool","tool":"memory_read","arguments":{"id":"A","event":true,"part":"transcript","limit":` + strconv.Itoa(m.step) + `}}`, nil
	case 5:
		return `{"action":"tool","tool":"memory_read","arguments":{"id":"A","event":true,"part":"transcript","cursor":"next-page","limit":1}}`, nil
	case 6:
		return `{"action":"tool","tool":"memory_read","arguments":{"id":"B","event":true,"part":"transcript","limit":1}}`, nil
	default:
		return `{"action":"reply","messages":["已保留可继续读取的位置。"]}`, nil
	}
}

func TestParameterRepairBudgetIsScopedToSourceAndPage(t *testing.T) {
	model, tools := &parameterRepairModel{}, &parameterReadTools{}
	e, err := NewPreview(enabledConfig(), model, "synthetic persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = tools, 8
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "读取历史"})
	if err != nil || d.Action != "reply" || model.step != 7 || fmt.Sprint(tools.calls) != "[A/ A/ A/ A/next-page B/]" {
		t.Fatal("parameter budget leaked across source/page or repeated a blocked call", d, err, model.step, tools.calls)
	}
}

func TestHistoryReadOfOneSourceDoesNotCompleteAnother(t *testing.T) {
	h := &historyMaterialNeed{IDs: map[string]bool{"A": true, "B": true}, MessageIDs: map[string]string{"native-A": "A", "native-B": "B"}, ReadIDs: map[string]bool{}}
	h.observeRead("memory_read", json.RawMessage(`{"event":true,"part":"transcript","id":"A"}`), `{"text":"合成正文","has_more":false}`)
	if !h.ReadIDs["A"] || h.ReadIDs["B"] {
		t.Fatal("one source completed two")
	}
	h.observeRead("video_inspect", json.RawMessage(`{"message_id":"native-A","action":"audio"}`), `{"text":"同一来源另一种读取"}`)
	if !h.ReadIDs["A"] || h.ReadIDs["B"] {
		t.Fatal("duplicate modality counted as second video")
	}
	h.observeRead("memory_read", json.RawMessage(`{"event":true,"part":"observation","id":"B"}`), `{"text":"部分正文","has_more":true}`)
	if h.ReadIDs["B"] {
		t.Fatal("partial page completed source")
	}
	h.observeRead("memory_read", json.RawMessage(`{"event":true,"part":"observation","id":"B"}`), `{"text":"完整正文","has_more":false}`)
	if !h.ReadIDs["A"] || !h.ReadIDs["B"] {
		t.Fatal("independent source reads not recognized")
	}
}

func TestHistoryReadFailureCanBeExplainedInsteadOfBecomingSilence(t *testing.T) {
	m := &historyModel{reportReadFailure: true}
	tools := &historyTools{failRead: true}
	e, err := NewPreview(enabledConfig(), m, "synthetic persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = tools
	e.MaxToolCalls = 5
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "总结本群前8条视频", QuotedSource: &SourceRef{MessageID: "anchor"}})
	if err != nil || d.Action != "reply" || d.Reason == "history_materials_not_inspected" || m.steps != 3 || len(tools.names) != 2 {
		t.Fatal("actual tool failure was swallowed by unread-history guard", d, err, m.steps, tools.names)
	}
}

func TestHistoryInspectionUsesVerifiedAssetOriginAndNamesOnlyUnreadSources(t *testing.T) {
	h := &historyMaterialNeed{IDs: map[string]bool{"A": true, "B": true}, MessageIDs: map[string]string{"native-A": "A", "native-B": "B"}, ReadIDs: map[string]bool{}}
	h.observeRead("video_inspect", json.RawMessage(`{"asset_id":"asset-A","action":"audio"}`), `{"source_message_id":"native-A","text":"合成音轨正文"}`)
	if h.ReadIDs["A"] != true || h.ReadIDs["B"] {
		t.Fatal("asset read lost origin or marked another source")
	}
	h.observeRead("video_inspect", json.RawMessage(`{"asset_id":"foreign","action":"audio"}`), `{"source_message_id":"foreign-native","text":"其他来源"}`)
	if h.ReadIDs["B"] {
		t.Fatal("unrelated asset completed request")
	}
	h.observeRead("memory_read", json.RawMessage(`{"message_id":"native-B","part":"transcript"}`), `{"event":{"id":"B"},"text":"另一条原文","has_more":false}`)
	if !h.ReadIDs["A"] || !h.ReadIDs["B"] {
		t.Fatal("resolved native original was not counted")
	}
}
