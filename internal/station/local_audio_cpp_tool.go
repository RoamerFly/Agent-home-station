package station

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/pluginhost"
)

const (
	localAudioCppModelsTool     = "project_audio_cpp_models"
	localAudioCppSynthesizeTool = "project_audio_cpp_synthesize"
	localAudioCppTranscribeTool = "project_audio_cpp_transcribe"
)

func (t *localToolSet) localAudioCppTools(ctx context.Context) []mcptools.Tool {
	if t.projectTools == nil || t.projectTools.audio == nil || t.run == nil || !t.run.Tracked || t.access == nil {
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
	if _, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localAudioCppPackage, capability: "audio-cpp.models"}); err != nil {
		return nil
	}
	info, err := t.projectTools.audio.Info(ctx, grant.FishID)
	if err != nil || info.Binding == "" {
		return nil
	}
	return []mcptools.Tool{
		mcptools.NewTool(localAudioCppModelsTool, "列出已登记的 audio.cpp 本机语音模型。", `{"type":"object","properties":{},"additionalProperties":false}`),
		mcptools.NewTool(localAudioCppSynthesizeTool, "使用本机 audio.cpp 生成 WAV 语音成品，不会发送。text 为正文，text_language 可留空。", `{"type":"object","properties":{"text":{"type":"string","maxLength":12000},"text_language":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		mcptools.NewTool(localAudioCppTranscribeTool, "用本机 audio.cpp 识别已登记的 WAV 成品。需要 artifact_id，不接受路径或任意字节。", `{"type":"object","properties":{"artifact_id":{"type":"string"}},"required":["artifact_id"],"additionalProperties":false}`),
	}
}

func (t *localToolSet) callLocalAudioCpp(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if t.projectTools == nil || t.projectTools.audio == nil || t.run == nil || !t.run.Tracked || t.access == nil {
		return "", errors.New("当前对话没有 audio.cpp 本机语音授权")
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	host, err := t.projectTools.forFish(grant.FishID)
	if err != nil {
		return "", err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localAudioCppPackage, capability: nameToAudioCppCapability(name)})
	if err != nil {
		return "", err
	}
	info, err := t.projectTools.audio.Info(ctx, grant.FishID)
	if err != nil || info.Binding == "" {
		return "", errors.New("请先在项目工具中登记并启用 audio.cpp")
	}
	switch name {
	case localAudioCppModelsTool:
		if err := strictLocalMemoryDecode(raw, &struct{}{}); err != nil {
			return "", errors.New("模型列表不需要参数")
		}
		return localProcessJSON(map[string]any{"models": info.Models, "backend": info.Backend, "version": info.Version})
	case localAudioCppSynthesizeTool:
		var in struct {
			Text     string `json:"text"`
			Language string `json:"text_language,omitempty"`
		}
		if len(raw) > 80<<10 || strictLocalMemoryDecode(raw, &in) != nil || strings.TrimSpace(in.Text) == "" {
			return "", mcptools.ArgumentError("$.text", "请提供要生成的正文")
		}
		if len(in.Text) > localTTSTextLimit || !utf8.ValidString(in.Text) || strings.ContainsRune(in.Text, 0) {
			return "", mcptools.ArgumentError("$.text", "正文需要是有效 UTF-8 且不超过 12000 字节")
		}
		source, ok := t.store.SourceForInput(companion.Input{Channel: "local", Account: grant.FishID, Thread: grant.OwnerID, Sender: grant.OwnerID, TraceID: t.traceID})
		if !ok {
			return "", errors.New("本轮来源已变化")
		}
		op := companion.LocalToolCheckpoint{Tool: name, Grant: grant.projectJobAuthorization(), Key: localCheckpointKey(name, source.EventID, source.Revision, in)}
		return t.dispatchCheckpointed(ctx, op, grant, func() (string, string, error) {
			job, err := t.createAudioCppJob(ctx, host, grant, installed, source, op, "audio-cpp.synthesize", info.Binding)
			if err != nil {
				return "", "", err
			}
			request := localTTSRequest{VoiceID: "kokoro", Text: in.Text, Language: in.Language}
			result, runErr := t.projectTools.audio.Synthesize(ctx, grant.FishID, info.Binding, request)
			if runErr != nil {
				_, _ = host.store.FailJob(ctx, job.ID, job.Revision, json.RawMessage(`{"reason":"audio_cpp_synthesis_failed"}`))
				return "", "", runErr
			}
			if sourceErr := t.run.CheckSource(ctx); sourceErr != nil || !t.access.current(grant) {
				_, _ = host.store.FinishJob(ctx, job.ID, job.Revision, "cancelled", nil)
				if sourceErr != nil {
					return "", "", sourceErr
				}
				return "", "", errors.New("本机家目录授权已变化")
			}
			artifact, pubErr := t.publishAudioCpp(ctx, grant, source, job.ID, result.WAV)
			if pubErr != nil {
				_, _ = host.store.FailJob(ctx, job.ID, job.Revision, json.RawMessage(`{"reason":"audio_cpp_artifact_registration_failed"}`))
				return "", "", pubErr
			}
			rate, seconds, _ := mediaspec.PCM16WAVInfo(result.WAV)
			receipt, _ := json.Marshal(map[string]any{"model": "kokoro", "sample_rate": rate, "duration_seconds": seconds, "sha256": artifact.SHA256, "bytes": artifact.Bytes})
			job, err = host.store.CompleteJob(ctx, job.ID, job.Revision, []string{artifact.ID}, receipt)
			if err != nil {
				return "", "", err
			}
			view, viewErr := localProcessJSON(t.localProjectResult(ctx, name, job))
			return view, "recorded", viewErr
		})
	case localAudioCppTranscribeTool:
		var in struct {
			ArtifactID string `json:"artifact_id"`
		}
		if len(raw) > 16<<10 || strictLocalMemoryDecode(raw, &in) != nil || in.ArtifactID == "" {
			return "", mcptools.ArgumentError("$.artifact_id", "请提供已登记的 WAV 成品编号")
		}
		bound, err := t.access.home.artifacts.bindProjectArtifact(ctx, in.ArtifactID, t.access, grant)
		if err != nil {
			return "", err
		}
		file, err := t.access.home.artifacts.open(ctx, bound.artifact, func() bool { return t.access.current(grant) })
		if err != nil {
			return "", err
		}
		wav, readErr := io.ReadAll(io.LimitReader(file, localAudioCppMaxWAV+1))
		_ = file.Close()
		if readErr != nil || len(wav) > localAudioCppMaxWAV {
			return "", errors.New("音频成品无法读取")
		}
		text, err := t.projectTools.audio.Transcribe(ctx, grant.FishID, info.Binding, wav)
		if err != nil {
			return "", err
		}
		if err := t.run.CheckSource(ctx); err != nil || !t.access.current(grant) {
			if err != nil {
				return "", err
			}
			return "", errors.New("本机家目录授权已变化")
		}
		return localProcessJSON(map[string]any{"text": text, "model": "sensevoice", "artifact_id": in.ArtifactID})
	default:
		return "", errors.New("未知的 audio.cpp 工具")
	}
}

func nameToAudioCppCapability(name string) string {
	switch name {
	case localAudioCppSynthesizeTool:
		return "audio-cpp.synthesize"
	case localAudioCppTranscribeTool:
		return "audio-cpp.transcribe"
	default:
		return "audio-cpp.models"
	}
}

func (t *localToolSet) createAudioCppJob(ctx context.Context, host *localProjectToolHost, grant localWorkspaceGrant, installed pluginhost.InstalledPackage, source *companion.SourceRef, op companion.LocalToolCheckpoint, capability, binding string) (pluginhost.Job, error) {
	cfg, err := host.store.Configuration(ctx, localAudioCppPackage)
	if err != nil {
		return pluginhost.Job{}, err
	}
	call := pluginhost.Invocation{Account: grant.FishID, Thread: grant.OwnerID, Requester: grant.OwnerID, Purpose: "audio.cpp local speech", RequestKey: op.Key, Authorization: op.Grant, SourceVersions: map[string]int{source.EventID: source.Revision}}
	if err := pluginhost.Authorize(installed.Package, pluginhost.Grant{Account: grant.FishID, Plugin: installed.Package.ID, Version: installed.Package.Version, Permissions: installed.Permissions}, call, localAudioCppGrants); err != nil {
		return pluginhost.Job{}, err
	}
	args, _ := json.Marshal(map[string]string{"binding": binding})
	job, created, err := host.store.CreateJob(ctx, pluginhost.Job{Plugin: localAudioCppPackage, Version: installed.Package.Version, PackageDigest: installed.Digest, ConfigRevision: strconv.Itoa(cfg.Revision), Context: call, Tool: capability, Arguments: args})
	if err != nil || !created {
		return job, err
	}
	return host.store.ClaimJob(ctx, job.ID, job.Revision)
}

func (t *localToolSet) publishAudioCpp(ctx context.Context, grant localWorkspaceGrant, source *companion.SourceRef, job string, wav []byte) (localArtifact, error) {
	parent := filepath.Join(t.access.home.dataDir, "project-tool-tasks")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return localArtifact{}, err
	}
	dir, err := os.MkdirTemp(parent, "audio-cpp-")
	if err != nil {
		return localArtifact{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "speech.wav")
	if err := os.WriteFile(path, wav, 0600); err != nil {
		return localArtifact{}, err
	}
	return t.access.home.artifacts.publishFile(ctx, localArtifactInput{Key: job, Expected: mediaspec.SpeechSHA256(wav), project: localAudioCppPackage}, source, t.access, grant, path, nil)
}
