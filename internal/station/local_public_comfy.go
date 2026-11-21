package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"wechat-bot/internal/comfyui"
)

type publicComfySubmission struct {
	Binding, Workflow, Approval, GraphSHA256, SaveNode string
	VideoOutput                                        bool
}

type publicComfyProduct struct {
	Images []comfyui.Image
	Video  comfyui.MP4
}

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

// submitPublicComfy takes an approved workflow request and an opaque caller
// job ID. Message provenance and delivery records remain with the application.
func (s *localProjectTools) submitPublicComfy(ctx context.Context, fish, jobID, workflowID string, inputs map[string]json.RawMessage) (publicComfySubmission, error) {
	var submission publicComfySubmission
	if s == nil || s.comfy == nil || workflowID == "" {
		return submission, errors.New("本机 ComfyUI 后端或工作流不可用")
	}
	s.update.Lock()
	defer s.update.Unlock()
	host, err := s.forFish(fish)
	if err != nil {
		return submission, err
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localComfyPackage, capability: localComfyCapability}); err != nil {
		return submission, err
	}
	deployment, workflows, err := s.comfySetup(ctx, fish)
	if err != nil {
		return submission, err
	}
	var selected *localComfyWorkflow
	for i := range workflows {
		if workflows[i].ID == workflowID {
			selected = &workflows[i]
			break
		}
	}
	if selected == nil {
		return submission, errors.New("指定的 ComfyUI 工作流未获批准")
	}
	graph, digest, err := selected.prepare(inputs)
	if err != nil {
		return submission, err
	}
	ref, err := comfyui.NewJobRef(jobID, selected.SaveNode)
	if err != nil {
		return submission, err
	}
	prepare := comfyui.Prepare
	video := selected.OutputKind == "video"
	if video {
		prepare = comfyui.PrepareMP4
	}
	prepared, err := prepare(graph, digest, ref)
	if err != nil {
		return submission, err
	}
	submission = publicComfySubmission{Binding: deployment.Binding, Workflow: workflowID, Approval: selected.Fingerprint,
		GraphSHA256: digest, SaveNode: selected.SaveNode, VideoOutput: video}
	err = s.comfy.Use(ctx, fish, deployment.Binding, true, func(client localComfyClient) error {
		return client.Submit(ctx, prepared)
	})
	return submission, err
}

// publicComfyStatus returns verified backend output bytes only for the exact
// submission selected by the caller. The application persists and delivers it.
func (s *localProjectTools) publicComfyStatus(ctx context.Context, fish, jobID string, submission publicComfySubmission) (comfyui.State, publicComfyProduct, error) {
	var product publicComfyProduct
	if s == nil || s.comfy == nil {
		return comfyui.Unknown, product, errors.New("本机 ComfyUI 后端不可用")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return comfyui.Unknown, product, err
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localComfyPackage, capability: localComfyCapability}); err != nil {
		return comfyui.Unknown, product, err
	}
	deployment, workflows, err := s.comfySetup(ctx, fish)
	if err != nil || deployment.Binding != submission.Binding {
		return comfyui.Unknown, product, errors.New("ComfyUI 部署已变化")
	}
	approved := false
	for _, workflow := range workflows {
		if workflow.ID == submission.Workflow && workflow.Fingerprint == submission.Approval && workflow.SaveNode == submission.SaveNode && (workflow.OutputKind == "video") == submission.VideoOutput {
			approved = true
			break
		}
	}
	if !approved {
		return comfyui.Unknown, product, errors.New("ComfyUI 工作流批准已变化")
	}
	ref, err := comfyui.NewJobRef(jobID, submission.SaveNode)
	if err != nil {
		return comfyui.Unknown, product, err
	}
	state := comfyui.Unknown
	err = s.comfy.Use(ctx, fish, submission.Binding, false, func(client localComfyClient) error {
		count := 0
		state, count, err = localComfyOutputStatus(ctx, client, ref, submission.VideoOutput)
		if err != nil || state != comfyui.Completed {
			return err
		}
		if submission.VideoOutput {
			if count != 1 {
				return errors.New("ComfyUI 视频成品数量无效")
			}
			videoClient, ok := client.(localComfyVideoClient)
			if !ok {
				return errors.New("ComfyUI 视频结果接口不可用")
			}
			product.Video, err = videoClient.FetchMP4(ctx, ref)
			return err
		}
		if count < 1 || count > localComfyImageLimit {
			return errors.New("ComfyUI 图片成品数量无效")
		}
		for i := range count {
			image, imageErr := client.FetchPNG(ctx, ref, i)
			if imageErr != nil {
				return imageErr
			}
			if image.Width < 1 || image.Height < 1 || len(image.Data) == 0 || image.SHA256 == "" {
				return fmt.Errorf("ComfyUI 图片 %d 无效", i+1)
			}
			product.Images = append(product.Images, image)
		}
		return nil
	})
	if err != nil {
		return state, publicComfyProduct{}, err
	}
	return state, product, nil
}

func (s *localProjectTools) cancelPublicComfy(ctx context.Context, fish, jobID string, submission publicComfySubmission) error {
	if s == nil || s.comfy == nil {
		return errors.New("本机 ComfyUI 后端不可用")
	}
	ref, err := comfyui.NewJobRef(jobID, submission.SaveNode)
	if err != nil {
		return err
	}
	return s.comfy.Use(ctx, fish, submission.Binding, false, func(client localComfyClient) error {
		_, err := client.Cancel(ctx, ref)
		return err
	})
}

func (s *localProjectTools) settlePublicComfy(ctx context.Context, fish, jobID string, submission publicComfySubmission) error {
	if s == nil || s.comfy == nil {
		return errors.New("本机 ComfyUI 后端不可用")
	}
	ref, err := comfyui.NewJobRef(jobID, submission.SaveNode)
	if err != nil {
		return err
	}
	return s.comfy.Use(ctx, fish, submission.Binding, false, func(client localComfyClient) error { return client.MarkSettled(ctx, ref) })
}
