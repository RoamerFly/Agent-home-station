package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
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
	if m.steps == 1 || m.ignore {
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
func TestHistoryDirectoryIsPrefetchedAndPrematureResendRequestCannotPass(t *testing.T) {
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
			if d.Action != "silent" || d.Reason != "history_materials_not_inspected" || m.steps != 2 {
				t.Fatal("unread request was mislabeled/completed", d, m.steps)
			}
		} else {
			if d.Action != "reply" || m.steps != 3 || len(tools.names) != 2 || tools.names[1] != "memory_read" {
				t.Fatal(d, m.steps, tools.names)
			}
		}
	}
}

func TestHistoryReadOfOneSourceDoesNotCompleteAnother(t *testing.T) {
	h := &historyMaterialNeed{IDs: map[string]bool{"A": true, "B": true}, MessageIDs: map[string]string{"native-A": "A", "native-B": "B"}, ReadIDs: map[string]bool{}}
	h.observeRead("memory_read", json.RawMessage(`{"event":true,"part":"transcript","id":"A"}`), `{"text":"合成正文","has_more":false}`)
	if h.ContentRead {
		t.Fatal("one source completed two")
	}
	h.observeRead("video_inspect", json.RawMessage(`{"message_id":"native-A","action":"audio"}`), `{"text":"同一来源另一种读取"}`)
	if h.ContentRead {
		t.Fatal("duplicate modality counted as second video")
	}
	h.observeRead("memory_read", json.RawMessage(`{"event":true,"part":"observation","id":"B"}`), `{"text":"部分正文","has_more":true}`)
	if h.ContentRead {
		t.Fatal("partial page completed source")
	}
	h.observeRead("memory_read", json.RawMessage(`{"event":true,"part":"observation","id":"B"}`), `{"text":"完整正文","has_more":false}`)
	if !h.ContentRead {
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
	h := &historyMaterialNeed{IDs: map[string]bool{"A": true}}
	h.observeReadFailure("memory_read", json.RawMessage(`{"event":true,"id":"unrelated"}`))
	if h.ReadFailed {
		t.Fatal("unrelated failure bypassed source guard")
	}
	h.observeReadFailure("memory_read", json.RawMessage(`{"event":true,"id":"A"}`))
	if !h.ReadFailed || h.ContentRead {
		t.Fatal("failed read was confused with inspected content")
	}
}
