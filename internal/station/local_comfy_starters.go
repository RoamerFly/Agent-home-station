package station

import (
	"embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"wechat-bot/internal/comfyui"
)

//go:embed comfy_starters/registry.json comfy_starters/image/*.json comfy_starters/video/*.json
var localComfyStarters embed.FS

// Install new templates once per home. Existing user graphs and records win.
func localComfyInstallStarters(root string) error {
	raw, err := localComfyStarters.ReadFile("comfy_starters/registry.json")
	if err != nil {
		return err
	}
	var starter localComfyWorkflowRegistry
	if err := localComfyStrictDecode(raw, &starter); err != nil {
		return err
	}
	if starter.ComfyVersion != comfyui.ReferenceVersion {
		return errors.New("ComfyUI 模板版本不匹配")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if !localComfySafeDirectory(root) {
		return errors.New("ComfyUI 工作流目录无效")
	}
	target := filepath.Join(root, localComfyWorkflowRegistryFile)
	registry := localComfyWorkflowRegistry{SchemaVersion: 1, ComfyVersion: comfyui.ReferenceVersion}
	if previous, err := os.ReadFile(target); err == nil {
		if err := localComfyStrictDecode(previous, &registry); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if registry.SchemaVersion != 1 || registry.ComfyVersion != comfyui.ReferenceVersion {
		return errors.New("现有 ComfyUI 工作流登记版本无效")
	}
	seen := make(map[string]bool, len(registry.Workflows))
	for _, item := range registry.Workflows {
		seen[item.ID] = true
	}
	changed := false
	for _, item := range starter.Workflows {
		if seen[item.ID] {
			continue
		}
		if !localComfyGraphName(item.Graph) {
			return errors.New("ComfyUI 模板路径无效")
		}
		path := filepath.Join(root, filepath.FromSlash(item.Graph))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if !localComfySafeDirectory(filepath.Dir(path)) {
			return errors.New("ComfyUI 模板目录无效")
		}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			data, err := localComfyStarters.ReadFile("comfy_starters/" + strings.ReplaceAll(item.Graph, "\\", "/"))
			if err != nil || localComfyDigest(data) != item.SHA256 {
				return errors.New("ComfyUI 模板校验失败")
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := localComfyBuildWorkflow(root, comfyui.ReferenceVersion, item); err != nil {
			continue
		} // user-owned file wins
		registry.Workflows = append(registry.Workflows, item)
		changed = true
	}
	if !changed {
		return nil
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	tmp := target + ".new"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	return os.Rename(tmp, target)
}
