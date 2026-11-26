package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"wechat-bot/internal/comfyui"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

func (e localComfyInvocation) jobAction(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		ID     string `json:"job_id"`
		Action string `json:"action"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || !validLocalRVCHex(in.ID, 64) || in.Action != "status" && in.Action != "cancel" {
		return "", errors.New("请提供原 ComfyUI 任务编号和查询或取消操作")
	}
	job, err := e.host.store.Job(ctx, in.ID)
	g := e.grant
	if err != nil || job.Plugin != localComfyPackage || job.Tool != localComfyCapability || job.Context.Account != g.FishID ||
		job.Context.Requester != g.OwnerID || job.Context.Thread != g.OwnerID || !g.matchesProjectJobAuthorization(job.Context.Authorization) ||
		job.PackageDigest != e.installed.Digest || job.ConfigRevision != e.configRevision || e.authorize(job.Context) != nil {
		return "", errors.New("ComfyUI 任务不存在或原授权、版本已变化")
	}
	var args localComfyJobInput
	if json.Unmarshal(job.Arguments, &args) != nil {
		return "", errors.New("ComfyUI 任务记录无效")
	}
	if in.Action == "status" {
		if err := e.checkSource(ctx, args); err != nil {
			return "", err
		}
	}
	if job.Recovery() == "terminal" {
		if in.Action == "cancel" {
			if job.State == "completed" && !job.CancelRequested {
				job, err = e.host.store.RevokeCompletedJob(ctx, job.ID, job.Revision)
				if err != nil {
					return "", err
				}
			}
			return localProcessJSON(comfyJobView(job))
		}
		return localProcessJSON(e.finishedView(job))
	}
	if in.Action == "cancel" && !job.CancelRequested {
		job, err = e.host.store.RequestJobCancel(ctx, job.ID, job.Revision)
		if err != nil {
			return "", err
		}
	}
	if job.State == "pending" || job.Recovery() == "terminal" {
		view := comfyJobView(job)
		if job.State == "pending" {
			view["reason"] = "backend_not_started"
		}
		return localProcessJSON(view)
	}
	if !job.CancelRequested {
		e.deployment, err = e.tool.projectTools.comfy.Info(ctx, g.FishID)
		if err != nil || e.checkWorkflow(ctx, args) != nil {
			view := comfyJobView(job)
			view["state"], view["reason"] = "needs_review", "deployment_or_workflow_changed"
			return localProcessJSON(view)
		}
	}
	ref, err := comfyui.NewJobRef(job.ID, args.SaveNode)
	if err != nil || job.RemoteID != "" && job.RemoteID != ref.ID {
		return "", errors.New("ComfyUI 原任务编号无法核对")
	}
	var view map[string]any
	err = e.tool.projectTools.comfy.Use(ctx, g.FishID, args.Binding, false, func(client localComfyClient) error {
		if !job.CancelRequested {
			if err := e.checkSource(ctx, args); err != nil {
				return err
			}
		}
		state, count, err := localComfyOutputStatus(ctx, client, ref, args.VideoOutput)
		if err != nil {
			return err
		}
		view, err = e.reconcile(ctx, client, job, args, ref, state, count)
		if err == nil {
			switch view["state"] {
			case "completed", "failed", "cancelled", "artifact_unavailable":
				_ = client.MarkSettled(ctx, ref)
			}
		}
		return err
	})
	if errors.Is(err, errLocalComfyNotRunning) {
		view = comfyJobView(job)
		if !job.CancelRequested {
			view["state"] = "needs_review"
		}
		view["reason"] = "original_backend_not_running"
	} else if err != nil {
		return "", err
	}
	return localProcessJSON(view)
}

type localComfyVideoClient interface {
	VideoStatus(context.Context, comfyui.JobRef) (comfyui.VideoStatus, error)
	FetchMP4(context.Context, comfyui.JobRef) (comfyui.MP4, error)
}

func localComfyOutputStatus(ctx context.Context, client localComfyClient, ref comfyui.JobRef, video bool) (comfyui.State, int, error) {
	if video {
		backend, ok := client.(localComfyVideoClient)
		if !ok {
			return comfyui.Unknown, 0, errors.New("ComfyUI 视频结果接口不可用")
		}
		status, err := backend.VideoStatus(ctx, ref)
		return status.State, status.VideoCount, err
	}
	status, err := client.Status(ctx, ref)
	return status.State, status.ImageCount, err
}

func (e localComfyInvocation) reconcile(ctx context.Context, client localComfyClient, job pluginhost.Job, args localComfyJobInput, ref comfyui.JobRef, state comfyui.State, count int) (map[string]any, error) {
	if state != comfyui.Unknown && job.RemoteID == "" {
		next, err := e.host.store.AcknowledgeJob(ctx, job.ID, job.Revision, ref.ID)
		if err != nil {
			return nil, err
		}
		job = next
	}
	if job.CancelRequested {
		return e.cancelComfyJob(ctx, client, job, ref, state, args.VideoOutput)
	}
	if err := e.checkSource(ctx, args); err != nil {
		return nil, err
	}
	switch state {
	case comfyui.Completed:
		if args.VideoOutput {
			if count != 1 {
				return comfyJobView(e.failComfyJob(ctx, job, "approved_mp4_output_count_invalid")), nil
			}
			return e.publishComfyVideo(ctx, client, job, args, ref)
		}
		if count < 1 || count > localComfyImageLimit {
			return comfyJobView(e.failComfyJob(ctx, job, "approved_png_output_count_invalid")), nil
		}
		return e.publishComfyImages(ctx, client, job, args, ref, count)
	case comfyui.Failed:
		return comfyJobView(e.failComfyJob(ctx, job, "workflow_execution_failed")), nil
	case comfyui.Cancelled:
		next, err := e.host.store.FinishJob(ctx, job.ID, job.Revision, "cancelled", nil)
		return comfyJobView(next), err
	case comfyui.Unknown:
		view := comfyJobView(job)
		view["state"], view["reason"] = "needs_review", "original_prompt_not_found"
		return view, nil
	default:
		view := comfyJobView(job)
		view["backend_state"] = state
		return view, nil
	}
}

func (e localComfyInvocation) cancelComfyJob(ctx context.Context, client localComfyClient, job pluginhost.Job, ref comfyui.JobRef, state comfyui.State, video bool) (map[string]any, error) {
	if state == comfyui.Pending || state == comfyui.Running {
		dispatched, err := client.Cancel(ctx, ref)
		if err != nil {
			return nil, err
		}
		next, _, err := localComfyOutputStatus(ctx, client, ref, video)
		if err != nil {
			return nil, err
		}
		// Removing a queued item from ComfyUI leaves no history entry.
		if dispatched && next == comfyui.Unknown {
			state = comfyui.Cancelled
		} else {
			state = next
		}
	}
	switch state {
	case comfyui.Cancelled:
		next, err := e.host.store.FinishJob(ctx, job.ID, job.Revision, "cancelled", nil)
		return comfyJobView(next), err
	case comfyui.Completed:
		next, err := e.host.store.CompleteJob(ctx, job.ID, job.Revision, nil, json.RawMessage(`{"output_withheld_after_cancel":true}`))
		return comfyJobView(next), err
	case comfyui.Failed:
		return comfyJobView(e.failComfyJob(ctx, job, "workflow_execution_failed")), nil
	default:
		view := comfyJobView(job)
		view["backend_state"] = state
		return view, nil
	}
}

func (e localComfyInvocation) failComfyJob(ctx context.Context, job pluginhost.Job, reason string) pluginhost.Job {
	receipt, _ := json.Marshal(map[string]string{"reason": reason})
	if next, err := e.host.store.FailJob(ctx, job.ID, job.Revision, receipt); err == nil {
		return next
	}
	return job
}

func (e localComfyInvocation) finishedView(job pluginhost.Job) map[string]any {
	view := comfyJobView(job)
	if len(job.Artifacts) == 0 || job.CancelRequested {
		return view
	}
	store := e.tool.access.home.artifacts
	artifacts := make([]map[string]any, 0, len(job.Artifacts))
	for _, id := range job.Artifacts {
		a, err := store.read(id)
		if err != nil || a.Request != job.ID || a.Fish != e.grant.FishID || a.Owner != e.grant.OwnerID {
			view["state"] = "artifact_unavailable"
			return view
		}
		artifacts = append(artifacts, a.view())
	}
	view["artifacts"] = artifacts
	if len(artifacts) == 1 {
		view["artifact_id"] = job.Artifacts[0]
		var output localComfyVideoReceipt
		if json.Unmarshal(job.Result, &output) == nil && output.Kind == "video" {
			view["video"] = output
		}
	}
	return view
}

func (e localComfyInvocation) publishComfyImages(ctx context.Context, client localComfyClient, job pluginhost.Job, args localComfyJobInput, ref comfyui.JobRef, count int) (map[string]any, error) {
	dir := filepath.Join(stationComfyOutputStageDir(e.tool.projectTools.home.dataDir), job.ID)
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = root.Close()
		_ = os.Remove(dir)
	}()
	ids := make([]string, 0, count)
	type imageEvidence struct {
		Width  int    `json:"width"`
		Height int    `json:"height"`
		SHA256 string `json:"sha256"`
	}
	evidence := make([]imageEvidence, 0, count)
	for index := range count {
		img, err := client.FetchPNG(ctx, ref, index)
		if err != nil {
			return nil, err
		}
		if err := e.checkSource(ctx, args); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("comfy-%02d.png", index+1)
		if err := stageComfyOutput(root, name, img.Data); err != nil {
			return nil, err
		}
		input := localArtifactInput{Key: job.ID, Expected: img.SHA256, project: localComfyPackage}
		a, err := e.tool.access.home.artifacts.publishFile(ctx, input, args.Origin, e.tool.access, e.grant, filepath.Join(dir, name), nil)
		_ = root.Remove(name)
		if err != nil {
			return nil, err
		}
		ids = append(ids, a.ID)
		evidence = append(evidence, imageEvidence{img.Width, img.Height, img.SHA256})
	}
	if err := e.checkSource(ctx, args); err != nil {
		return nil, err
	}
	if err := e.checkWorkflow(ctx, args); err != nil {
		return nil, err
	}
	receipt, _ := json.Marshal(map[string]any{"workflow_id": args.Workflow, "images": evidence})
	next, err := e.host.store.CompleteJob(ctx, job.ID, job.Revision, ids, receipt)
	if err != nil {
		return nil, err
	}
	return e.finishedView(next), nil
}

type localComfyVideoReceipt struct {
	Workflow string  `json:"workflow_id"`
	Kind     string  `json:"output_kind"`
	SHA256   string  `json:"sha256"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Frames   int     `json:"frame_count,omitempty"`
	Seconds  float64 `json:"duration_seconds"`
	FPS      float64 `json:"fps,omitempty"`
}

func (e localComfyInvocation) publishComfyVideo(ctx context.Context, client localComfyClient, job pluginhost.Job, args localComfyJobInput, ref comfyui.JobRef) (map[string]any, error) {
	backend, ok := client.(localComfyVideoClient)
	if !ok {
		return nil, errors.New("ComfyUI 视频结果接口不可用")
	}
	video, err := backend.FetchMP4(ctx, ref)
	if err != nil {
		return nil, err
	}
	// The deterministic staging path is part of the artifact identity. Two
	// status queries must not replace each other's MP4 or decoder output.
	e.host.comfyVideoPublish.Lock()
	defer e.host.comfyVideoPublish.Unlock()
	current, err := e.host.store.Job(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if current.Recovery() == "terminal" {
		return e.finishedView(current), nil
	}
	if current.Revision != job.Revision {
		return nil, errors.New("ComfyUI 视频任务状态已变化，请重新查询")
	}
	dir := filepath.Join(stationComfyOutputStageDir(e.tool.projectTools.home.dataDir), job.ID)
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return nil, err
	}
	const name = "comfy-video.mp4"
	defer func() {
		_ = root.Remove("frame-000001.jpg")
		_ = root.Remove(name)
		_ = root.Close()
		_ = os.Remove(dir)
	}()
	if err := stageComfyOutput(root, name, video.Data); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, name)
	meta, err := mediadecode.Probe(ctx, file)
	if err != nil {
		return nil, err
	}
	if meta.Width < 1 || meta.Height < 1 || !localComfyFinite(meta.FPS) || meta.FPS <= 0 {
		return nil, errors.New("ComfyUI 输出的视频尺寸或帧率无效")
	}
	frames, _, err := mediadecode.ExtractFramesSized(ctx, file, dir, 0, 1, 1, 64)
	if err != nil || len(frames) != 1 {
		return nil, errors.New("ComfyUI 输出的视频无法解码")
	}
	if err := e.checkSource(ctx, args); err != nil {
		return nil, err
	}
	if err := e.checkWorkflow(ctx, args); err != nil {
		return nil, err
	}
	input := localArtifactInput{Key: job.ID, Expected: video.SHA256, project: localComfyPackage}
	artifact, err := e.tool.access.home.artifacts.publishFile(ctx, input, args.Origin, e.tool.access, e.grant, file, nil)
	if err != nil {
		return nil, err
	}
	receipt, _ := json.Marshal(localComfyVideoReceipt{
		Workflow: args.Workflow, Kind: "video", SHA256: video.SHA256, Width: meta.Width, Height: meta.Height,
		Frames: meta.Frames, Seconds: meta.Seconds, FPS: meta.FPS,
	})
	next, err := e.host.store.CompleteJob(ctx, job.ID, job.Revision, []string{artifact.ID}, receipt)
	if err != nil {
		return nil, err
	}
	return e.finishedView(next), nil
}

func stageComfyOutput(root *os.Root, name string, data []byte) error {
	if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		_ = root.Remove(name)
	}
	return err
}
