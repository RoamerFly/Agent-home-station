package admin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

func (p *permittedTools) createWorkspaceProject(ctx context.Context, raw json.RawMessage) (string, error) {
	return p.callProjectPlugin(ctx, "project_create", raw)
}

// mediateWorkspaceDirectory creates exactly one child beneath the host-selected
// writable root. Naming and workflow semantics belong to the installed package.
func (p *permittedTools) mediateWorkspaceDirectory(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var in struct {
		Name   string `json:"name"`
		Shared bool   `json:"shared"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Name == "" || in.Name == "." || in.Name == ".." || filepath.Base(in.Name) != in.Name || strings.ContainsAny(in.Name, "/\\:\x00") {
		return nil, toolInputError("directory must be one local child name")
	}
	mode := p.manager.agent.config().Host.Mode
	if !p.workspaceExecutionAllowed() || mode != "files" && mode != "full" {
		return nil, toolInputError("workspace file write permission required")
	}
	x := p.executionContext()
	base := x.Conversation
	if in.Shared {
		if !p.privileged() {
			return nil, toolInputError("shared workspace requires trusted owner")
		}
		base = x.Public
	}
	dir := filepath.Join(base, "projects", in.Name)
	if err := ensureManaged(dir); err != nil {
		return nil, err
	}
	return map[string]any{"path": dir, "shared": in.Shared, "existing_files_preserved": true}, nil
}
