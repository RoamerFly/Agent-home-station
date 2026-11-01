package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"unicode/utf8"
)

// Native bounds and enums remain authoritative at the live service. Host
// preflight checks the administrator-installed declarative JSON shape only.
type musicSampling map[string]map[string]any

func (p *permittedTools) validateMusicSampling(ctx context.Context, s musicSampling, planning string) error {
	if len(s) == 0 && planning == "" {
		return nil
	}
	resources, err := p.manager.musicCatalogResources(ctx, musicNativeSamplingFile)
	if err != nil {
		return toolInputError("music_plugin_catalog_unavailable")
	}
	definitions, err := musicNativeDefinitions(resources[musicNativeSamplingFile])
	if err != nil {
		return toolInputError("music_plugin_catalog_invalid")
	}
	values := map[string]any{}
	if s != nil {
		values["sampling"] = s
	}
	if planning != "" {
		values["planning_mode"] = planning
	}
	for name, value := range values {
		var schema map[string]any
		raw, ok := definitions[name]
		if !ok || json.Unmarshal(raw, &schema) != nil {
			return toolInputError("music_plugin_catalog_invalid")
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return toolInputError("音乐参数无法编码：" + name)
		}
		var canonical any
		if json.Unmarshal(encoded, &canonical) != nil || validateMusicDeclaredShape(canonical, schema) != nil {
			return toolInputError("音乐参数不匹配已安装插件声明：" + name + "；请先调用music_sampling_options核对实际原生参数")
		}
	}
	return nil
}

func validateMusicDeclaredShape(value any, schema map[string]any) error {
	invalid := errors.New("music_plugin_parameter_shape_invalid")
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return invalid
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return invalid
		}
		for name, item := range object {
			definition, declared := properties[name]
			if !declared {
				if schema["additionalProperties"] == false {
					return invalid
				}
				continue
			}
			child, ok := definition.(map[string]any)
			if !ok || validateMusicDeclaredShape(item, child) != nil {
				return invalid
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return invalid
		}
		if minimum, ok := schema["minLength"].(float64); ok && utf8.RuneCountInString(text) < int(minimum) {
			return invalid
		}
	case "number", "integer":
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || schema["type"] == "integer" && number != math.Trunc(number) {
			return invalid
		}
	default:
		return invalid
	}
	if choices, ok := schema["enum"].([]any); ok {
		encoded, _ := json.Marshal(value)
		for _, choice := range choices {
			candidate, _ := json.Marshal(choice)
			if string(encoded) == string(candidate) {
				return nil
			}
		}
		return invalid
	}
	return nil
}
