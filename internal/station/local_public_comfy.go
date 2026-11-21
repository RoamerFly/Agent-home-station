package station

import (
	"context"
	"errors"
)

// publicComfyWorkflows reads the workstation's one installed workflow catalog.
// The caller owns message admission and delivery; no application source enters
// this operation.
func (s *localProjectTools) publicComfyWorkflows(ctx context.Context, fish string) ([]localComfyWorkflowView, error) {
	if s == nil || s.comfy == nil {
		return nil, errors.New("本机 ComfyUI 后端未登记")
	}
	s.update.Lock()
	defer s.update.Unlock()
	host, err := s.forFish(fish)
	if err != nil {
		return nil, err
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localComfyPackage, capability: localComfyCapability}); err != nil {
		return nil, err
	}
	_, workflows, err := s.comfySetup(ctx, fish)
	if err != nil {
		return nil, err
	}
	return localComfyWorkflowViews(workflows), nil
}
