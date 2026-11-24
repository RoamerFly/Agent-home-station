package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"wechat-bot/internal/comfyui"
)

// comfyRegister is the public ComfyUI tool's authoring entry point. The agent
// writes a graph in its own home; this function validates and registers it.
func (s *localProjectTools) comfyRegister(ctx context.Context, fish string, raw json.RawMessage) (string, error) {
	var record localComfyWorkflowRecord
	if localComfyStrictDecode(raw, &record) != nil || record.ID == "" || record.Graph == "" || record.SaveNode == "" {
		return "", errors.New("ComfyUI 工作流登记参数无效")
	}
	identity, err := s.home.identity.Read()
	if err != nil || identity.FishID != fish || identity.LocalOwner == nil {
		return "", errors.New("ComfyUI 家目录身份不可用")
	}
	home, err := s.home.directory(identity)
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, "workflows", "comfyui")
	if !localComfySafeDirectory(root) || !localComfyGraphName(record.Graph) {
		return "", errors.New("请先在大肥鱼工作区写入工作流图文件")
	}
	graph, err := localComfyReadRegular(filepath.Join(root, record.Graph), localComfyWorkflowMaxJSON)
	if err != nil {
		return "", err
	}
	record.SHA256 = localComfyDigest(graph)
	workflow, err := localComfyBuildWorkflow(root, comfyui.ReferenceVersion, record)
	if err != nil {
		return "", err
	}
	registration := localComfyWorkflowRegistry{SchemaVersion: 1, ComfyVersion: comfyui.ReferenceVersion}
	if previous, readErr := localComfyReadRegular(filepath.Join(root, localComfyWorkflowRegistryFile), localComfyWorkflowMaxJSON); readErr == nil {
		if localComfyStrictDecode(previous, &registration) != nil || registration.SchemaVersion != 1 || registration.ComfyVersion != comfyui.ReferenceVersion {
			return "", errors.New("现有工作流登记文件无效，请先修复")
		}
	} else if !os.IsNotExist(readErr) {
		return "", readErr
	}
	for _, existing := range registration.Workflows {
		if existing.ID == record.ID {
			continue
		}
		if _, err := localComfyBuildWorkflow(root, comfyui.ReferenceVersion, existing); err != nil {
			return "", err
		}
	}
	if s.comfy != nil {
		deployment, err := s.comfy.Info(ctx, fish)
		if err != nil {
			return "", err
		}
		installed, err := loadLocalComfyWorkflows(deployment.WorkflowRoot)
		if err != nil {
			return "", err
		}
		for _, item := range installed {
			if item.ID == record.ID {
				return "", errors.New("工作流编号已由公共工具使用")
			}
		}
	}
	replaced := false
	for i := range registration.Workflows {
		if registration.Workflows[i].ID == record.ID {
			registration.Workflows[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		registration.Workflows = append(registration.Workflows, record)
	}
	if len(registration.Workflows) > 64 {
		return "", errors.New("工作流登记数量超出支持范围")
	}
	sort.Slice(registration.Workflows, func(i, j int) bool { return registration.Workflows[i].ID < registration.Workflows[j].ID })
	data, err := json.MarshalIndent(registration, "", "  ")
	if err != nil || len(data) > localComfyWorkflowMaxJSON {
		return "", errors.New("工作流登记内容过大")
	}
	target := filepath.Join(root, localComfyWorkflowRegistryFile)
	tmp := target + "." + strconv.FormatInt(time.Now().UnixNano(), 36) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	return localProcessJSON(map[string]any{"registered": workflow.view(), "updated_existing": replaced, "workflow_count": len(registration.Workflows)})
}
