package admin

import "encoding/json"

// Native numeric ranges and sampler enums come from music_sampling_options;
// this layer rejects typos without replacing explicit values with defaults.
type musicSampling map[string]map[string]any

func (s musicSampling) validate() error {
	allowed := map[string]map[string]bool{
		"semantic": {"temperature": true, "top_p": true, "top_k": true, "repetition_penalty": true},
		"render":   {"steps": true, "cfg": true, "sampler_name": true, "scheduler": true, "denoise": true},
		"score":    {"max_abc_tokens": true, "temperature": true, "top_p": true, "top_k": true, "repetition_penalty": true, "penalty_window": true},
	}
	for group, values := range s {
		keys, ok := allowed[group]
		if !ok || values == nil {
			return toolInputError("未知采样参数组；请先调用music_sampling_options")
		}
		for key, value := range values {
			if !keys[key] {
				return toolInputError("未知采样参数：" + group + "." + key)
			}
			if key == "sampler_name" || key == "scheduler" {
				if v, ok := value.(string); !ok || v == "" {
					return toolInputError("采样器/调度器名称无效")
				}
			} else if _, ok := value.(float64); !ok {
				return toolInputError("采样数值无效：" + group + "." + key)
			}
		}
	}
	return nil
}

func musicSamplingSchema() map[string]any {
	groups := map[string]any{}
	for group, keys := range map[string][]string{
		"semantic": {"temperature", "top_p", "top_k", "repetition_penalty"},
		"render":   {"steps", "cfg", "sampler_name", "scheduler", "denoise"},
		"score":    {"max_abc_tokens", "temperature", "top_p", "top_k", "repetition_penalty", "penalty_window"},
	} {
		fields := map[string]any{}
		for _, key := range keys {
			kind := "number"
			if key == "sampler_name" || key == "scheduler" {
				kind = "string"
			}
			if key == "steps" || key == "top_k" || key == "max_abc_tokens" || key == "penalty_window" {
				kind = "integer"
			}
			fields[key] = map[string]any{"type": kind}
		}
		groups[group] = map[string]any{"type": "object", "properties": fields, "additionalProperties": false}
	}
	return map[string]any{"type": "object", "description": "Optional native overrides. Query music_sampling_options for live native limits, enum choices, meanings and workflow defaults. Explicit zero values are preserved. score applies to original planning only.", "properties": groups, "additionalProperties": false}
}

func extendMusicSamplingSchema(raw string) string {
	var schema map[string]any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		panic(err)
	}
	properties := schema["properties"].(map[string]any)
	properties["sampling"] = musicSamplingSchema()
	properties["planning_mode"] = map[string]any{"type": "string", "enum": []string{"off", "melody", "full"}, "description": "Original song ABC planning: keep workflow default if omitted. Covers use the reference score and reference_mode instead."}
	result, _ := json.Marshal(schema)
	return string(result)
}
