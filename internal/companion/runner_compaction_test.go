package companion

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestActionCompactionPreservesToolPairsAndLatestFollowup(t *testing.T) {
	turns := []map[string]string{{"role": "user", "content": `{"kind":"current_message","text":"发一首歌"}`}, {"role": "user", "content": `{"kind":"current_followup","events":[{"text":"改发原文件"}]}`}}
	for i := 0; i < 70; i++ {
		call, _ := json.Marshal(map[string]any{"action": "tool", "tool": "probe", "arguments": map[string]int{"index": i}})
		result, _ := json.Marshal(map[string]any{"tool": "probe", "source": "untrusted_tool_result", "result": fmt.Sprint(i), "execution": map[string]string{"status": "succeeded"}})
		turns = append(turns, map[string]string{"role": "assistant", "content": string(call)}, map[string]string{"role": "user", "content": string(result)})
	}
	compacted := compactActionTurns("policy", turns)
	if !strings.Contains(currentMessage(compacted), "改发原文件") {
		t.Fatal("latest structured followup was compressed away")
	}
	for i, turn := range compacted {
		var result struct{ Source string }
		if json.Unmarshal([]byte(turn["content"]), &result) == nil && result.Source == "untrusted_tool_result" {
			if i == 0 || compacted[i-1]["role"] != "assistant" {
				t.Fatal("orphaned tool result after compaction")
			}
		}
	}
}
