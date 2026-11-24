package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
)

func TestWindowCompactionKeepsCurrentFollowupAndToolReceipts(t *testing.T) {
	turns := []map[string]string{{"role": "user", "content": `{"kind":"current_message","text":"继续完成这首歌"}`}}
	for i := 0; i < 18; i++ {
		if i == 8 {
			turns = append(turns, map[string]string{"role": "user", "content": `{"kind":"current_followup","text":"改用新的声线"}`})
		}
		call, _ := json.Marshal(map[string]any{"action": "tool", "tool": "probe", "arguments": map[string]int{"page": i}})
		result, _ := json.Marshal(map[string]any{"tool": "probe", "source": "untrusted_tool_result", "result": strings.Repeat("真实中文结果", 180), "execution": map[string]any{"status": "succeeded", "result_id": fmt.Sprintf("saved-%02d", i)}})
		turns = append(turns, map[string]string{"role": "assistant", "content": string(call)}, map[string]string{"role": "user", "content": string(result)})
	}
	tools := []byte(`[{"name":"probe","description":"synthetic"}]`)
	kept := compactActionTurnsForWindow("system", turns, tools, 2200, 300)
	if err := ValidateContextWindow("system", kept, tools, 2200, 300); err != nil {
		t.Fatal(err)
	}
	followupKept := false
	for _, turn := range kept {
		if strings.Contains(turn["content"], `"kind":"current_followup"`) && strings.Contains(turn["content"], "改用新的声线") {
			followupKept = true
		}
	}
	if !followupKept || !strings.Contains(kept[0]["content"], "current_message") {
		t.Fatal("current instruction or latest follow-up lost")
	}
	if !strings.Contains(fmt.Sprint(kept), "saved-") {
		t.Fatal("operation receipt was lost")
	}
	for i, turn := range kept {
		if toolFeedbackTurn(turn) && (i == 0 || kept[i-1]["role"] != "assistant") {
			t.Fatal("orphaned tool feedback", i)
		}
	}
}

func TestWindowCompactionDoesNotShortenCurrentRequest(t *testing.T) {
	request := `{"kind":"current_message","text":"` + strings.Repeat("当前内容", 800) + `"}`
	kept := compactActionTurnsForWindow("system", []map[string]string{{"role": "user", "content": request}}, nil, 100, 20)
	if len(kept) != 1 || kept[0]["content"] != request || ValidateContextWindow("system", kept, nil, 100, 20) == nil {
		t.Fatal("current request should remain intact and report a real window limit")
	}
}

func TestWindowCompactionShortHistoryTriesSmallerTail(t *testing.T) {
	current := map[string]string{"role": "user", "content": `{"kind":"current_message","text":"继续整理报告"}`}
	followup := map[string]string{"role": "user", "content": `{"kind":"current_followup","text":"保留最后的结果编号"}`}
	recentCall := map[string]string{"role": "assistant", "content": `{"action":"tool","tool":"probe","arguments":{}}`}
	recentResult := map[string]string{"role": "user", "content": `{"source":"untrusted_tool_result","tool":"probe","result":"最新结果","execution":{"result_id":"recent"}}`}
	oldCall := map[string]string{"role": "assistant", "content": `{"action":"tool","tool":"probe","arguments":{"text":"` + strings.Repeat("旧参数", 1200) + `"}}`}
	oldResult := map[string]string{"role": "user", "content": `{"source":"untrusted_tool_result","tool":"probe","result":"已完成"}`}
	turns := []map[string]string{current, oldCall, oldResult, followup, recentCall, recentResult}
	kept := compactActionTurnsForWindow("system", turns, nil, 700, 80)
	if err := ValidateContextWindow("system", kept, nil, 700, 80); err != nil {
		t.Fatal(err)
	}
	if len(kept) != 5 || kept[0]["content"] != current["content"] ||
		kept[2]["content"] != followup["content"] ||
		kept[3]["content"] != recentCall["content"] || kept[4]["content"] != recentResult["content"] {
		t.Fatal("lost the current request, latest follow-up or most recent function/result pair")
	}
}

type repeatedDiscoveryModel struct{ nativeCalls, finalCalls int }

func (m *repeatedDiscoveryModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.finalCalls++
	return `{"action":"reply","messages":["目录页没有新变化，我已停下重复检查。"]}`, nil
}
func (m *repeatedDiscoveryModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	m.nativeCalls++
	if m.nativeCalls > 5 {
		return "", fmt.Errorf("repeated discovery loop did not stop")
	}
	return `{"action":"tool","tool":"discover_tools","arguments":{"query":"catalog"}}`, nil
}

type repeatedDiscoveryTools struct{}

func (*repeatedDiscoveryTools) AutonomousActions() bool { return true }
func (*repeatedDiscoveryTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "catalog_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (*repeatedDiscoveryTools) Call(context.Context, string, json.RawMessage) (string, error) {
	return "", fmt.Errorf("discovery should remain local")
}

func TestRepeatedDiscoveryStopsWithoutInfiniteAutonomousCalls(t *testing.T) {
	model := &repeatedDiscoveryModel{}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = &repeatedDiscoveryTools{}
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "查看工具目录"})
	if err != nil || d.Action != "reply" || model.nativeCalls != 3 || model.finalCalls != 1 {
		t.Fatal(d, err, model.nativeCalls, model.finalCalls)
	}
}
