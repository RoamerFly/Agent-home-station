package admin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

var workspaceProjectName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
var reservedProjectName = regexp.MustCompile(`^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)

// This is a convenience over the existing file tools, not a new permission.
// Directory creation uses the same pinned, symlink-rejecting managed roots.
func (p *permittedTools) createWorkspaceProject(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Name   string `json:"name"`
		Shared bool   `json:"shared"`
	}
	if json.Unmarshal(raw, &in) != nil || !workspaceProjectName.MatchString(in.Name) {
		return "", toolInputError("project name must be 1-48 lowercase letters, digits, _ or -, starting with a letter")
	}
	// Windows device names are reserved even without extensions.
	if reservedProjectName.MatchString(strings.ToLower(in.Name)) {
		return "", toolInputError("reserved project name")
	}
	mode := p.manager.agent.config().Host.Mode
	if !p.workspaceExecutionAllowed() || (mode != "files" && mode != "full") {
		return "", toolInputError("project creation requires workspace file write permission")
	}
	x := p.executionContext()
	base := x.Conversation
	if in.Shared {
		if !p.privileged() {
			return "", toolInputError("shared projects require the trusted owner")
		}
		base = x.Public
	}
	dir := filepath.Join(base, "projects", in.Name)
	if err := ensureManaged(dir); err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"path": dir, "shared": in.Shared, "existing_files_preserved": true,
		"next_tools":         []string{"server_list", "server_read", "server_write", "server_edit"},
		"guide":              map[string]any{"tool": "workspace_read", "shared": true, "name": "self-tools.md"},
		"execution_requires": "configured full mode and an available isolated runtime"})
	return string(b), err
}
