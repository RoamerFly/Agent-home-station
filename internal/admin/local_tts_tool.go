package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

const (
	localTTSPackage    = "station-speech"
	localTTSCapability = "station-speech.synthesize"
	localTTSVoicesTool = "project_speech_voices"
	localTTSStartTool  = "project_speech_synthesize"
	localTTSWAVLimit   = 32 << 20
	localTTSTextLimit  = 12000
)

var localTTSGrants = []string{"artifact.write", "gpu"}

func localTTSSetupReason(err error) string {
	if err == nil {
		return "尚未登记可用的本机说话声线。"
	}
	switch err.Error() {
	case "station_speech_not_installed_or_authorized":
		return "尚未登记或启用本机语音后端。"
	case "station_speech_runtime_unavailable":
		return "Windows 语音隔离运行器缺失或校验失败。"
	case "station_speech_runner_changed":
		return "语音合成脚本与登记的文件摘要不一致。"
	case "station_speech_voice_registration_invalid", "station_speech_prompt_changed", "station_speech_no_registered_voices":
		return "请核对已登记的说话声线、参考录音和参考转写。"
	default:
		return "请核对本机语音运行环境及部署登记。"
	}
}

func (t *localToolSet) localTTSTools(ctx context.Context) []mcptools.Tool {
	if t.projectTools == nil || t.projectTools.tts == nil || t.run == nil || !t.run.Tracked {
		return nil
	}
	grant, err := t.access.resolve()
	if err != nil {
		return nil
	}
	host, err := t.projectTools.forFish(grant.FishID)
	if err != nil {
		return nil
	}
	if _, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localTTSPackage, capability: localTTSCapability}); err != nil {
		return nil
	}
	if info, err := t.projectTools.tts.Info(ctx, grant.FishID); err != nil || len(info.Voices) == 0 {
		return nil
	}
	caps, err := host.store.Capabilities(ctx, localTTSPackage)
	if err != nil {
		return nil
	}
	var tools []mcptools.Tool
	for _, cap := range caps {
		if !cap.Discoverable {
			continue
		}
		name := ""
		switch cap.ID {
		case "station-speech.voices":
			name = localTTSVoicesTool
		case localTTSCapability:
			name = localTTSStartTool
		}
		if name != "" {
			tools = append(tools, mcptools.Tool{Name: name, Description: cap.Description, InputSchema: cap.InputSchema})
		}
	}
	return tools
}

func (t *localToolSet) callLocalTTS(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	s := t.projectTools
	if s == nil || s.tts == nil || t.run == nil || !t.run.Tracked || t.run.CheckSource(ctx) != nil {
		return "", errors.New("当前对话没有本机语音合成授权")
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	host, err := s.forFish(grant.FishID)
	if err != nil {
		return "", err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localTTSPackage, capability: localTTSCapability})
	if err != nil {
		return "", err
	}
	info, err := s.tts.Info(ctx, grant.FishID)
	if err != nil || info.Binding == "" {
		return "", errors.New("请在项目工具核对本机语音后端和获准声线")
	}
	if name == localTTSVoicesTool {
		if err := strictLocalMemoryDecode(raw, &struct{}{}); err != nil {
			return "", errors.New("声线列表不需要参数")
		}
		return localProcessJSON(map[string]any{"voices": info.Voices})
	}
	var request localTTSRequest
	if len(raw) > 80<<10 || strictLocalMemoryDecode(raw, &request) != nil {
		return "", argumentError("$", "请提供声线编号和要合成的正文")
	}
	if !utf8.ValidString(request.Text) || strings.TrimSpace(request.Text) == "" || strings.ContainsRune(request.Text, 0) {
		return "", argumentError("$.text", "需要有效的非空正文")
	}
	if len(request.Text) > localTTSTextLimit {
		return "", argumentError("$.text", "单次合成最多 12000 UTF-8 字节，可以分段合成")
	}
	if request.Language != "" && !localTTSLanguageAllowed(request.Language) {
		return "", argumentError("$.text_language", "语言模式不受支持，可查看工具声明或留空使用声线登记值")
	}
	var selected localTTSVoice
	for _, voice := range info.Voices {
		if voice.ID == request.VoiceID {
			selected = voice
			break
		}
	}
	if selected.Version == "" {
		return "", argumentError("$.voice_id", "指定声线未登记或已撤权，请查看可用声线列表")
	}
	if request.Speed == nil {
		normal := 1.0
		request.Speed = &normal
	}
	if *request.Speed < selected.SpeedMin || *request.Speed > selected.SpeedMax {
		return "", argumentError("$.speed", "语速超出该声线支持范围，请查看声线列表中的上下限")
	}
	source, ok := t.store.SourceForInput(companion.Input{Channel: "local", Account: grant.FishID, Thread: grant.OwnerID, Sender: grant.OwnerID, TraceID: t.traceID})
	if !ok {
		return "", errors.New("本轮来源已变化")
	}
	op := companion.LocalToolCheckpoint{Tool: localTTSStartTool, Grant: localCheckpointKey(grant),
		Key: localCheckpointKey(localTTSStartTool, source.EventID, source.Revision, request)}
	return t.dispatchCheckpointed(ctx, op, grant, func() (string, string, error) {
		job, err := t.runLocalTTS(ctx, s, host, grant, installed, source, op, info.Binding, selected, request)
		if err != nil {
			return "", "", err
		}
		view, err := localProcessJSON(t.ttsResult(ctx, job))
		return view, "registered", err
	})
}

func (t *localToolSet) runLocalTTS(ctx context.Context, s *localProjectTools, host *localProjectToolHost, grant localWorkspaceGrant, installed pluginhost.InstalledPackage, source *companion.SourceRef, op companion.LocalToolCheckpoint, binding string, selected localTTSVoice, request localTTSRequest) (pluginhost.Job, error) {
	s.update.Lock()
	job, jobCtx, release, err := t.claimLocalTTS(ctx, s, host, grant, installed, source, op, binding, selected, request)
	s.update.Unlock()
	if err != nil || release == nil {
		return job, err
	}
	defer release()
	result, err := s.tts.Synthesize(jobCtx, grant.FishID, binding, request)
	s.update.Lock()
	defer s.update.Unlock()
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if jobCtx.Err() != nil || !t.access.current(grant) || t.run.CheckSource(jobCtx) != nil {
		return host.store.FinishJob(save, job.ID, job.Revision, "cancelled", nil)
	}
	if err != nil {
		reason := "local_speech_synthesis_failed"
		switch err.Error() {
		case "station_speech_voice_changed", "station_speech_registered_resource_changed", "station_speech_deployment_changed":
			reason = "local_speech_registration_changed"
		case "station_speech_output_invalid", "station_speech_audio_missing":
			reason = "local_speech_invalid_wav"
		case "station_speech_runner_binding_mismatch", "station_speech_runner_result_invalid":
			reason = "local_speech_voice_receipt_mismatch"
		case "station_speech_runtime_unavailable":
			reason = "local_speech_runtime_unavailable"
		}
		receipt, _ := json.Marshal(map[string]string{"reason": reason})
		return host.store.FailJob(save, job.ID, job.Revision, receipt)
	}
	if result.VoiceID != selected.ID || result.Version != selected.Version || result.Speed != *request.Speed {
		return host.store.FailJob(save, job.ID, job.Revision, json.RawMessage(`{"reason":"local_speech_voice_receipt_mismatch"}`))
	}
	rate, seconds, audioErr := namedSpeechWAVInfo(result.WAV)
	if audioErr != nil || len(result.WAV) > localTTSWAVLimit || rate < 8000 || rate > 192000 {
		return host.store.FailJob(save, job.ID, job.Revision, json.RawMessage(`{"reason":"local_speech_invalid_wav"}`))
	}
	artifact, err := t.publishLocalTTS(jobCtx, grant, source, job.ID, result.WAV)
	if jobCtx.Err() != nil || !t.access.current(grant) || t.run.CheckSource(jobCtx) != nil {
		return host.store.FinishJob(save, job.ID, job.Revision, "cancelled", nil)
	}
	if err != nil {
		return host.store.FailJob(save, job.ID, job.Revision, json.RawMessage(`{"reason":"local_speech_artifact_registration_failed"}`))
	}
	receipt, err := json.Marshal(map[string]any{"voice_id": result.VoiceID, "voice_version": result.Version, "speed": result.Speed,
		"sample_rate": rate, "duration_seconds": seconds, "sha256": artifact.SHA256, "bytes": artifact.Bytes})
	if err != nil {
		return job, err
	}
	return host.store.CompleteJob(save, job.ID, job.Revision, []string{artifact.ID}, receipt)
}

// Called under the project update lock. Settings can revoke the in-flight
// invocation without waiting for the process or committing its output.
func (t *localToolSet) claimLocalTTS(ctx context.Context, s *localProjectTools, host *localProjectToolHost, grant localWorkspaceGrant, installed pluginhost.InstalledPackage, source *companion.SourceRef, op companion.LocalToolCheckpoint, binding string, selected localTTSVoice, request localTTSRequest) (pluginhost.Job, context.Context, func(), error) {
	var empty pluginhost.Job
	current, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localTTSPackage, capability: localTTSCapability})
	if err != nil || current.Revision != installed.Revision || s.ttsCancel != nil || !t.access.current(grant) || t.run.CheckSource(ctx) != nil {
		return empty, nil, nil, errors.New("语音工具正忙，或原请求与授权已变化")
	}
	cfg, err := host.store.Configuration(ctx, localTTSPackage)
	if err != nil {
		return empty, nil, nil, err
	}
	call := pluginhost.Invocation{Account: grant.FishID, Thread: grant.OwnerID, Requester: grant.OwnerID, Purpose: "local speech synthesis", RequestKey: op.Key, Authorization: op.Grant,
		SourceVersions: map[string]int{source.EventID: source.Revision}}
	if err := pluginhost.Authorize(current.Package, pluginhost.Grant{Account: grant.FishID, Plugin: localTTSPackage, Version: current.Package.Version, Permissions: current.Permissions}, call, localTTSGrants); err != nil {
		return empty, nil, nil, err
	}
	arguments, err := json.Marshal(map[string]any{"voice_id": selected.ID, "voice_version": selected.Version, "binding": binding,
		"speed": *request.Speed, "text_sha256": namedSpeechSHA([]byte(request.Text)), "text_language": request.Language})
	if err != nil {
		return empty, nil, nil, err
	}
	job, created, err := host.store.CreateJob(ctx, pluginhost.Job{Plugin: localTTSPackage, Version: current.Package.Version,
		PackageDigest: current.Digest, ConfigRevision: strconv.Itoa(cfg.Revision), Context: call, Tool: localTTSCapability, Arguments: arguments})
	if err != nil || !created {
		return job, nil, nil, err
	}
	job, err = host.store.ClaimJob(ctx, job.ID, job.Revision)
	if err != nil {
		return job, nil, nil, err
	}
	jobCtx, stop := context.WithCancel(ctx)
	s.ttsCancel = stop
	finished := s.markActive(localTTSPackage)
	return job, jobCtx, func() {
		stop()
		s.update.Lock()
		s.ttsCancel = nil
		finished()
		s.update.Unlock()
	}, nil
}

func (t *localToolSet) publishLocalTTS(ctx context.Context, grant localWorkspaceGrant, source *companion.SourceRef, job string, wav []byte) (localArtifact, error) {
	parent := filepath.Join(t.access.home.dataDir, "project-tool-tasks")
	root, err := workspacefs.EnsurePath(parent)
	if err != nil {
		return localArtifact{}, err
	}
	if err := root.Close(); err != nil {
		return localArtifact{}, err
	}
	dir, err := os.MkdirTemp(parent, "speech-")
	if err != nil {
		return localArtifact{}, err
	}
	path := filepath.Join(dir, "speech.wav")
	defer func() { _ = os.Remove(path); _ = os.Remove(dir) }()
	if err := os.WriteFile(path, wav, 0600); err != nil {
		return localArtifact{}, err
	}
	input := localArtifactInput{Key: job, Expected: namedSpeechSHA(wav), project: localTTSPackage}
	return t.access.home.artifacts.publishFile(ctx, input, source, t.access, grant, path, nil)
}

func (t *localToolSet) ttsResult(ctx context.Context, job pluginhost.Job) map[string]any {
	view := t.localProjectResult(ctx, localTTSStartTool, job)
	if job.State == "submitting" {
		view["state"] = "needs_review"
	}
	if job.State == "failed" || job.State == "completed" && view["artifact_id"] != nil {
		view["verification"] = json.RawMessage(job.Result)
	}
	return view
}

func (t *localToolSet) ttsCheckpointView(op companion.LocalToolCheckpoint, grant localWorkspaceGrant) map[string]any {
	view := map[string]any{"tool": op.Tool, "state": "needs_review", "recovered": true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if t.projectTools == nil || t.run.CheckSource(ctx) != nil || !t.access.current(grant) {
		return view
	}
	host, err := t.projectTools.forFish(grant.FishID)
	if err != nil {
		return view
	}
	job, err := host.store.JobByRequest(ctx, localTTSPackage, grant.OwnerID, op.Key)
	if err != nil || job.Tool != localTTSCapability || job.Context.Authorization != op.Grant || job.Context.Account != grant.FishID || job.Context.Requester != grant.OwnerID {
		return view
	}
	view = t.ttsResult(ctx, job)
	view["recovered"] = true
	return view
}
