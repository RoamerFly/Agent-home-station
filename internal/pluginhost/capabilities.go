package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Capability comes from the approved package snapshot, never tools/list output
// from a running process. A plugin cannot grant itself a new tool or permission.
type Capability struct {
	ID             string          `json:"id"`
	Implementation string          `json:"implementation"`
	Description    string          `json:"description"`
	Permissions    []string        `json:"permissions"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema"`
	Discoverable   bool            `json:"discoverable"`
	Async          bool            `json:"async,omitempty"`
	SideEffect     string          `json:"side_effect,omitempty"`
}

var capabilityID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,127}$`)

func (s *Store) Capabilities(ctx context.Context, id string) ([]Capability, error) {
	raw, err := s.PackageFile(ctx, id, "capabilities.json")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Version      int          `json:"version"`
		Capabilities []Capability `json:"capabilities"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || doc.Version != 1 {
		return nil, errors.New("unsupported capability declaration")
	}
	i, err := s.Installed(ctx, id)
	if err != nil {
		return nil, err
	}
	declared, seen := map[string]bool{}, map[string]bool{}
	for _, p := range i.Package.Permissions {
		declared[p] = true
	}
	for _, c := range doc.Capabilities {
		if !capabilityID.MatchString(c.ID) || seen[c.ID] || strings.TrimSpace(c.Description) == "" {
			return nil, errors.New("invalid or repeated capability")
		}
		seen[c.ID] = true
		if c.Implementation != "ready" && c.Implementation != "migration-pending" && c.Implementation != "template" {
			return nil, errors.New("invalid capability implementation status")
		}
		if c.Discoverable && c.Implementation != "ready" {
			return nil, errors.New("unimplemented capability cannot be discovered")
		}
		for _, p := range c.Permissions {
			if !declared[p] {
				return nil, errors.New("capability permission not declared by package")
			}
		}
		for _, schema := range []json.RawMessage{c.InputSchema, c.OutputSchema} {
			var obj map[string]json.RawMessage
			if json.Unmarshal(schema, &obj) != nil || obj == nil || string(obj["type"]) != `"object"` {
				return nil, errors.New("capability schema must describe an object")
			}
		}
	}
	return doc.Capabilities, nil
}
