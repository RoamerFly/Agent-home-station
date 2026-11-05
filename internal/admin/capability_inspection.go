package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

type CapabilityPathCheck struct {
	Field string `json:"field"`
	State string `json:"state"`
}
type RegisteredCapability struct {
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	Registered        bool                  `json:"registered"`
	ClaimedValidation string                `json:"claimed_validation,omitempty"`
	Paths             []CapabilityPathCheck `json:"paths"`
	ResourceState     string                `json:"resource_state"`
	ExecutionVerified bool                  `json:"execution_verified"`
}
type CapabilityInspection struct {
	Capabilities []RegisteredCapability `json:"capabilities"`
	Unavailable  []projectManifestIssue `json:"unavailable_manifests"`
	HostMode     string                 `json:"host_mode"`
	Backend      string                 `json:"backend"`
	ReadOnly     bool                   `json:"read_only"`
	Instruction  string                 `json:"instruction"`
}

// InspectRegisteredCapabilities reads registration and path metadata only. It
// never imports a model, runs a command, trains, connects to a provider or sends.
// A manifest's historical validation label is deliberately not live readiness.
func (m *Manager) InspectRegisteredCapabilities(ctx context.Context) (CapabilityInspection, error) {
	result := CapabilityInspection{Capabilities: []RegisteredCapability{}, Unavailable: []projectManifestIssue{}, HostMode: m.agent.config().Host.Mode, Backend: "linux", ReadOnly: true, Instruction: "登记和文件存在不代表本次执行或交付成功；execution_verified只表示本次检查是否实际执行，本接口始终为false"}
	if m.agent.config().Host.Windows != nil {
		result.Backend = "windows"
	}
	public := filepath.Join(m.managedRoot(), "public_workspace")
	if _, err := os.Stat(filepath.Join(public, "manifests")); os.IsNotExist(err) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	items, issues, err := readProjectManifests(ctx, public, "", false)
	if err != nil {
		return result, err
	}
	result.Unavailable = issues
	for _, raw := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var value map[string]any
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		id, _ := value["id"].(string)
		name, _ := value["name"].(string)
		claim, _ := value["runtime_status"].(string)
		row := RegisteredCapability{ID: id, Name: name, Registered: true, ClaimedValidation: claim, Paths: []CapabilityPathCheck{}, ResourceState: "metadata_only"}
		for _, field := range []string{"root", "entry", "python", "readme", "base_models", "models", "training_entry", "training_python_paths"} {
			v, exists := value[field]
			if !exists {
				continue
			}
			paths := []string{}
			if path, ok := v.(string); ok {
				paths = append(paths, path)
			} else if list, ok := v.([]any); ok {
				for _, item := range list {
					if path, ok := item.(string); ok {
						paths = append(paths, path)
					}
				}
			}
			if len(paths) == 0 {
				row.Paths = append(row.Paths, CapabilityPathCheck{field, "invalid"})
				row.ResourceState = "unavailable"
				continue
			}
			for _, path := range paths {
				state := "available"
				if !filepath.IsAbs(path) || !pathWithin(public, path) {
					state = "outside_managed_workspace"
				} else if info, e := os.Stat(path); e != nil {
					state = "missing_or_unreadable"
				} else if e = regularMusicPath(path, info.IsDir()); e != nil {
					state = "linked_or_unreadable"
				}
				row.Paths = append(row.Paths, CapabilityPathCheck{field, state})
				if state != "available" {
					row.ResourceState = "unavailable"
				} else if row.ResourceState == "metadata_only" {
					row.ResourceState = "paths_available"
				}
			}
		}
		result.Capabilities = append(result.Capabilities, row)
	}
	return result, nil
}
