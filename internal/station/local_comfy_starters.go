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

// The first published H3 image-to-video starter used a reference-video node.
// That node treats the picture as a visual reference, not as the first frame.
// Migrate only the exact shipped graph; a user-edited graph remains theirs.
const oldH3ImageToVideoSHA = "3a9c40671db56920fd0e0cc98b2816d4f39f08ce34463690fc05e30e92928314"

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
	seen := make(map[string]int, len(registry.Workflows))
	for i, item := range registry.Workflows {
		seen[item.ID] = i + 1
	}
	changed := false
	for _, item := range starter.Workflows {
		if index := seen[item.ID]; index != 0 {
			migrated, err := localComfyMigrateStarter(root, registry.Workflows[index-1], item)
			if err != nil {
				return err
			}
			if migrated {
				registry.Workflows[index-1] = item
				changed = true
			}
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

func localComfyMigrateStarter(root string, previous, next localComfyWorkflowRecord) (bool, error) {
	if previous.ID != "h3-image-to-video" || previous.Graph != "video/h3-image-to-video.json" ||
		previous.SHA256 != oldH3ImageToVideoSHA || next.ID != previous.ID || next.Graph != previous.Graph {
		return false, nil
	}
	path := filepath.Join(root, filepath.FromSlash(previous.Graph))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	digest := localComfyDigest(current)
	if digest != oldH3ImageToVideoSHA && digest != next.SHA256 {
		return false, nil
	}
	if digest == oldH3ImageToVideoSHA {
		updated, err := localComfyStarters.ReadFile("comfy_starters/" + next.Graph)
		if err != nil || localComfyDigest(updated) != next.SHA256 {
			return false, errors.New("ComfyUI 图生视频模板校验失败")
		}
		file, err := os.CreateTemp(filepath.Dir(path), "h3-image-to-video-migrate-*.json")
		if err != nil {
			return false, err
		}
		tmp := file.Name()
		defer os.Remove(tmp)
		if _, err := file.Write(updated); err != nil {
			_ = file.Close()
			return false, err
		}
		if err := file.Close(); err != nil {
			return false, err
		}
		validation := next
		validation.Graph = filepath.ToSlash(filepath.Join("video", filepath.Base(tmp)))
		if _, err := localComfyBuildWorkflow(root, comfyui.ReferenceVersion, validation); err != nil {
			return false, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return false, err
		}
	}
	if _, err := localComfyBuildWorkflow(root, comfyui.ReferenceVersion, next); err != nil {
		return false, err
	}
	return true, nil
}
