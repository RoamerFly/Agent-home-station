package admin

import (
	"context"
	"encoding/json"
)

// Only a current, compact public registration index enters each round. Detailed
// manuals and private model files are still retrieved on demand by tools.
func (p *permittedTools) currentProjectIndex(ctx context.Context) []map[string]string {
	rows := []map[string]string{}
	items, _, err := readProjectManifests(ctx, p.executionContext().Public, "", false)
	if err != nil {
		return rows
	}
	for _, raw := range items {
		var row struct {
			ID, Name string
			Status   string `json:"runtime_status"`
		}
		if json.Unmarshal(raw, &row) == nil && row.ID != "" {
			rows = append(rows, map[string]string{"id": row.ID, "name": row.Name, "registered_runtime_status": row.Status, "details_tool": "project_read"})
		}
	}
	return rows
}
