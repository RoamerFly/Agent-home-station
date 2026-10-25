package companion

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Native providers may return multiple calls even with parallel calls disabled.
// Validate the whole batch before executing any call. The regular tool loop
// applies authorization, budgets, provenance and durable claims to each item.
func expandToolBatch(answer string, allowed map[string]bool) ([]string, error) {
	var batch struct {
		Action string
		Calls  []json.RawMessage
	}
	if json.Unmarshal([]byte(answer), &batch) != nil || batch.Action != "tool_batch" {
		return nil, nil
	}
	if len(answer) > 32<<10 || len(batch.Calls) < 2 || len(batch.Calls) > 8 {
		return nil, errors.New("invalid_native_tool_batch")
	}
	result := []string{}
	for _, raw := range batch.Calls {
		var call struct {
			Action, Tool string
			Arguments    json.RawMessage
		}
		if json.Unmarshal(raw, &call) != nil || call.Action != "tool" || !allowed[call.Tool] {
			return nil, errors.New("invalid_native_tool_batch")
		}
		args := bytes.TrimSpace(call.Arguments)
		if len(args) == 0 || len(args) > 16<<10 || args[0] != '{' || !json.Valid(args) {
			return nil, errors.New("invalid_native_tool_batch")
		}
		result = append(result, string(raw))
	}
	return result, nil
}
