package admin

import (
	"context"
	"encoding/json"
	"os"
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
	result := map[string]any{"path": dir, "shared": in.Shared, "existing_files_preserved": true}
	if !in.Shared {
		return result, nil
	}
	// Public projects must be discoverable in later Agent turns without making
	// the manifests control directory writable to arbitrary file operations.
	// The fixed, non-overwriting registration is created by the same owner-only
	// operation that created the public project, so it cannot name another path
	// or smuggle host configuration into the registry.
	manifests := filepath.Join(x.Public, "manifests")
	if err := ensureManaged(manifests); err != nil {
		return nil, err
	}
	registration, err := json.Marshal(map[string]string{
		"id":    in.Name,
		"name":  in.Name,
		"entry": filepath.ToSlash(filepath.Join("projects", in.Name)),
	})
	if err != nil {
		return nil, err
	}
	manifest := filepath.Join(manifests, in.Name+".json")
	// Publish only a fully flushed immutable file. os.Link is an atomic,
	// no-replace publication on the same managed filesystem: an interrupted
	// write leaves only the unreferenced temporary file, never a truncated
	// registration visible to project_list.
	temporary, err := os.CreateTemp(manifests, "."+in.Name+"-*.pending")
	if err != nil {
		return nil, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0600); err != nil {
		temporary.Close()
		return nil, err
	}
	if _, err = temporary.Write(registration); err != nil {
		temporary.Close()
		return nil, err
	}
	if err = temporary.Sync(); err != nil {
		temporary.Close()
		return nil, err
	}
	if err = temporary.Close(); err != nil {
		return nil, err
	}
	if err = os.Link(temporaryName, manifest); err == nil {
		result["registered"] = true
	} else if os.IsExist(err) {
		result["registered"] = false
		result["registration_existing"] = true
	} else {
		return nil, err
	}
	return result, nil
}
