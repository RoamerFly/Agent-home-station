package station

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s *localProjectTools) comfyModels(ctx context.Context, fish string, raw json.RawMessage) (string, error) {
	var in struct {
		Category string `json:"category"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || in.Offset < 0 || in.Limit < 0 {
		return "", errLocalComfyWorkflowSetup
	}
	if in.Category == "" {
		in.Category = "checkpoints"
	}
	allowed := map[string]bool{"checkpoints": true, "clip": true, "clip_vision": true, "diffusion_models": true, "loras": true, "text_encoders": true, "vae": true, "controlnet": true, "upscale_models": true}
	if !allowed[in.Category] {
		return "", errLocalComfyWorkflowSetup
	}
	if in.Limit == 0 {
		in.Limit = 100
	}
	view, err := s.readComfyDeployment()
	if err != nil || !view.Configured || view.Configuration == nil {
		return "", errLocalComfyWorkflowSetup
	}
	registration := view.Configuration
	_ = fish // Caller already checked the current fish grant and installed tool.
	roots := []string{filepath.Join(registration.Models, in.Category)}
	if registration.ModelLibrary != "" {
		for _, kind := range []string{"image", "video"} {
			roots = append(roots, filepath.Join(registration.ModelLibrary, kind, in.Category))
		}
	}
	rows := make([]map[string]string, 0)
	for _, root := range roots {
		if !localComfySafeDirectory(root) {
			continue
		}
		area := "built-in"
		if strings.HasPrefix(root, registration.ModelLibrary+string(os.PathSeparator)) {
			area = "model-library"
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type().IsRegular() && (strings.EqualFold(filepath.Ext(entry.Name()), ".safetensors") || strings.EqualFold(filepath.Ext(entry.Name()), ".ckpt") || strings.EqualFold(filepath.Ext(entry.Name()), ".pt") || strings.EqualFold(filepath.Ext(entry.Name()), ".pth")) {
				rel, _ := filepath.Rel(root, path)
				rows = append(rows, map[string]string{"name": filepath.ToSlash(rel), "source": area})
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["name"] < rows[j]["name"] })
	end := min(len(rows), in.Offset+in.Limit)
	if in.Offset > len(rows) {
		in.Offset = len(rows)
	}
	return localProcessJSON(map[string]any{"category": in.Category, "items": rows[in.Offset:end], "total": len(rows), "next_offset": end, "has_more": end < len(rows)})
}
