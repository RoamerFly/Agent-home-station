package pluginhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// UISchema is declarative data for the host renderer. It deliberately has no
// URLs, scripts, templates, or arbitrary bindings.
type UISchema struct {
	Version  int         `json:"version"`
	Sections []UISection `json:"sections"`
}
type UISection struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Component string `json:"component"`
	Binding   string `json:"binding,omitempty"`
	Action    string `json:"action,omitempty"`
}

var allowedUIComponents = map[string]bool{"configuration": true, "tasks": true, "resources": true, "diagnostics": true}
var allowedUIBindings = map[string]bool{"host_data.settings": true, "host_data.tasks": true, "host_data.resources": true, "host_data.diagnostics": true}
var allowedUIActions = map[string]bool{"settings.save": true, "devices.list": true, "tasks.create": true, "tasks.cancel": true, "tasks.stop": true}

var componentUIBindings = map[string]string{
	"configuration": "host_data.settings",
	"tasks":         "host_data.tasks",
	"resources":     "host_data.resources",
	"diagnostics":   "host_data.diagnostics",
}

func componentUIAction(component, action string) bool {
	if action == "" {
		return true
	}
	switch component {
	case "configuration":
		return action == "settings.save"
	case "tasks":
		return action == "tasks.create" || action == "tasks.cancel" || action == "tasks.stop"
	case "diagnostics":
		return action == "devices.list"
	}
	return false
}

func ParseUISchema(raw []byte) (UISchema, error) {
	if len(raw) == 0 || len(raw) > 128<<10 {
		return UISchema{}, errors.New("ui schema invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var schema UISchema
	if err := decoder.Decode(&schema); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return UISchema{}, errors.New("ui schema invalid")
	}
	if schema.Version != 1 || len(schema.Sections) > 16 {
		return UISchema{}, errors.New("ui schema invalid")
	}
	seen := map[string]bool{}
	components := map[string]bool{}
	for _, section := range schema.Sections {
		if !identifier.MatchString(section.ID) || section.Title == "" || len(section.Title) > 80 || !allowedUIComponents[section.Component] || seen[section.ID] || section.Binding != "" && !allowedUIBindings[section.Binding] || section.Action != "" && !allowedUIActions[section.Action] {
			return UISchema{}, errors.New("ui schema invalid")
		}
		// A declaration may select supported host controls, but cannot relabel
		// a destructive task action as a configuration save or bind a form to
		// unrelated private data. Version 1 renders each component once.
		if components[section.Component] || section.Binding != "" && section.Binding != componentUIBindings[section.Component] || !componentUIAction(section.Component, section.Action) {
			return UISchema{}, errors.New("ui schema component binding invalid")
		}
		seen[section.ID] = true
		components[section.Component] = true
	}
	return schema, nil
}
