package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/workspacefs"
)

type publicAudioPrepareRequest struct {
	Filename string   `json:"filename,omitempty"`
	Start    float64  `json:"start_seconds,omitempty"`
	Duration *float64 `json:"duration_seconds,omitempty"`
	Channels string   `json:"channels"`
}

type publicAudioPrepareResult struct {
	WAV          []byte
	Name         string
	Coverage     localAudioCoverage
	Verification localAudioVerification
}

type publicAudioPreparePolicy struct {
	host                             *localProjectToolHost
	installed                        pluginhost.InstalledPackage
	configRevision                   int
	invocation                       pluginhost.Invocation
	input, output, inputHash, taskID string
	request                          publicAudioPrepareRequest
	result                           publicAudioPrepareResult
}

func (s *localProjectTools) publicAudioPrepareBinding(ctx context.Context, fish string) (string, error) {
	if s == nil || !s.runtimeReady() {
		return "", errors.New("音频工坊 Windows 运行时不可用")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return "", err
	}
	installed, err := installedLocalProject(ctx, host, projectOperation(localAudioToolName))
	if err != nil {
		return "", err
	}
	if !slices.Contains(installed.Permissions, "artifact.read") || !slices.Contains(installed.Permissions, "artifact.write") {
		return "", errors.New("音频工坊缺少输入或成品授权")
	}
	configuration, err := host.store.Configuration(ctx, localAudioPackage)
	if err != nil {
		return "", err
	}
	return localCheckpointKey(installed.Digest, installed.Revision, configuration.Revision), nil
}

func (p *publicAudioPreparePolicy) Check(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cap.ID != localAudioCapability || cap.Async || job.Plugin != localAudioPackage || job.Tool != localAudioCapability ||
		job.PackageDigest != p.installed.Digest || job.Version != p.installed.Package.Version ||
		job.ConfigRevision != strconv.Itoa(p.configRevision) || !reflect.DeepEqual(job.Context, p.invocation) {
		return errors.New("音频工坊任务声明已变化")
	}
	current, err := installedLocalProject(ctx, p.host, projectOperation(localAudioToolName))
	config, configErr := p.host.store.Configuration(ctx, localAudioPackage)
	if err != nil || configErr != nil || current.Revision != p.installed.Revision || config.Revision != p.configRevision || p.host.controller.Closed() {
		return errors.New("音频工坊授权或配置已变化")
	}
	if job.ID != "" {
		fresh, err := p.host.store.Job(ctx, job.ID)
		if err != nil || fresh.CancelRequested || fresh.Revision != job.Revision {
			return errors.New("音频工坊任务状态已变化")
		}
	}
	return nil
}

func (p *publicAudioPreparePolicy) CallMetadata(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) (map[string]json.RawMessage, error) {
	if err := p.Check(ctx, job, cap); err != nil {
		return nil, err
	}
	data, err := json.Marshal(map[string]any{"input": map[string]string{"artifact_id": p.taskID, "sha256": p.inputHash,
		"path": windowsProjectToolPath(p.input), "mime_type": "audio/wav"},
		"input_root": windowsProjectToolPath(filepath.Dir(p.input)), "output_root": windowsProjectToolPath(p.output), "output_dir": windowsProjectToolPath(p.output)})
	return map[string]json.RawMessage{"station/audio": data}, err
}

func (p *publicAudioPreparePolicy) Validate(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	var empty pluginruntime.VerifiedResult
	if err := p.Check(ctx, job, cap); err != nil {
		return empty, err
	}
	var out struct {
		OK          bool                `json:"ok"`
		Error       string              `json:"error"`
		InputSHA256 string              `json:"input_sha256"`
		Coverage    *localAudioCoverage `json:"coverage"`
		Outputs     []struct {
			Path         string                 `json:"path"`
			Name         string                 `json:"name"`
			MIMEType     string                 `json:"mime_type"`
			SHA256       string                 `json:"sha256"`
			Size         int64                  `json:"size"`
			Verification localAudioVerification `json:"verification"`
		} `json:"outputs"`
	}
	if len(raw) > 32<<10 || json.Unmarshal(raw, &out) != nil {
		return empty, errors.New("音频工坊返回格式无效")
	}
	if !out.OK && len(out.Outputs) == 0 && safeLocalAudioCode(out.Error) {
		value, _ := json.Marshal(map[string]string{"reason": out.Error})
		return pluginruntime.VerifiedResult{Failed: true, Result: value}, nil
	}
	if !out.OK || out.InputSHA256 != p.inputHash || len(out.Outputs) != 1 || out.Coverage == nil {
		return empty, errors.New("音频工坊结果与输入不匹配")
	}
	entry := out.Outputs[0]
	info, coverage := entry.Verification, *out.Coverage
	rate := float64(info.SampleRate)
	if rate == 0 || entry.MIMEType != "audio/wav" || !validLocalRVCHex(entry.SHA256, 64) ||
		coverage.Frames != info.Frames || float64(coverage.StartFrame) != math.Floor(p.request.Start*rate) ||
		coverage.StartSeconds != float64(coverage.StartFrame)/rate || coverage.DurationSeconds != float64(info.Frames)/rate ||
		(p.request.Duration == nil) != (coverage.RequestedSeconds == nil) ||
		p.request.Channels == "mono" && info.Channels != 1 {
		return empty, errors.New("音频工坊覆盖或声道回执无效")
	}
	if p.request.Duration != nil {
		limit := math.Ceil(*p.request.Duration * rate)
		if *coverage.RequestedSeconds != *p.request.Duration || float64(info.Frames) > limit || !coverage.EOF && float64(info.Frames) != limit {
			return empty, errors.New("音频工坊时长回执无效")
		}
	} else if !coverage.EOF {
		return empty, errors.New("音频工坊没有处理到源音频末尾")
	}
	rel, err := filepath.Rel(windowsProjectToolPath(p.output), windowsProjectToolPath(entry.Path))
	if err != nil || !filepath.IsAbs(entry.Path) || rel != entry.Name || !filepath.IsLocal(rel) || !mediaspec.SafeFilename(rel) ||
		rel != filepath.Base(rel) || !strings.EqualFold(filepath.Ext(rel), ".wav") {
		return empty, errors.New("音频工坊输出路径无效")
	}
	root, err := workspacefs.OpenPath(p.output)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, rel)
	if err != nil {
		return empty, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.Size() > stationAttachmentLimit || verifyLocalPCM(file, stat.Size(), entry.Size, info) != nil {
		return empty, errors.New("音频工坊输出 WAV 未通过核验")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return empty, err
	}
	wav, err := io.ReadAll(io.LimitReader(file, stationAttachmentLimit+1))
	if err != nil || int64(len(wav)) != stat.Size() || externalDigest(wav) != strings.ToLower(entry.SHA256) {
		return empty, errors.New("音频工坊输出内容已变化")
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return empty, err
	}
	p.result = publicAudioPrepareResult{WAV: wav, Name: rel, Coverage: coverage, Verification: info}
	receipt, _ := json.Marshal(map[string]any{"coverage": coverage, "verification": info, "sha256": entry.SHA256, "bytes": entry.Size})
	return pluginruntime.VerifiedResult{Result: receipt}, nil
}

// Process a single caller-owned WAV stream in a task-scoped installed LPAC
// runtime. No WeChat source, sender, thread or artifact path enters the tool.
func (s *localProjectTools) publicAudioPrepare(ctx context.Context, fish, binding, taskID, inputHash string, inputBytes int64, input io.Reader, request publicAudioPrepareRequest) (publicAudioPrepareResult, error) {
	var empty publicAudioPrepareResult
	if s == nil || !s.runtimeReady() || !validLocalRVCHex(taskID, 64) || !validLocalRVCHex(inputHash, 64) ||
		inputBytes <= 0 || inputBytes > stationAttachmentLimit || input == nil {
		return empty, errors.New("音频工坊输入或运行时无效")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return empty, err
	}
	s.update.Lock()
	installed, err := installedLocalProject(ctx, host, projectOperation(localAudioToolName))
	if err != nil {
		s.update.Unlock()
		return empty, err
	}
	if !slices.Contains(installed.Permissions, "artifact.read") || !slices.Contains(installed.Permissions, "artifact.write") {
		s.update.Unlock()
		return empty, errors.New("音频工坊缺少输入或成品授权")
	}
	release := s.markActive(localAudioPackage)
	s.update.Unlock()
	defer release()
	configuration, err := host.store.Configuration(ctx, localAudioPackage)
	if err != nil {
		return empty, err
	}
	if binding == "" || binding != localCheckpointKey(installed.Digest, installed.Revision, configuration.Revision) {
		return empty, errors.New("音频工坊登记已变化")
	}
	options := s.options
	options.DocumentRuntimeRoot = ""
	controller, inputPath, outputPath, cleanup, err := openScopedProjectRuntime(ctx, filepath.Join(s.home.dataDir, "project-tool-tasks"),
		host.store, options, localAudioPackage, true, func(ctx context.Context, dir string) (localProjectArtifactMaterialized, error) {
			path := filepath.Join(dir, "input.wav")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return localProjectArtifactMaterialized{}, err
			}
			hash := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(input, inputBytes+1))
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || n != inputBytes || hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(inputHash) || ctx.Err() != nil {
				return localProjectArtifactMaterialized{}, errors.New("音频工坊输入字节与已核验原件不一致")
			}
			return localProjectArtifactMaterialized{InputPath: path, ArtifactID: taskID, SHA256: inputHash, Bytes: n}, nil
		})
	if err != nil {
		return empty, err
	}
	defer cleanup()
	invocation := pluginhost.Invocation{Account: fish, Thread: "host:audio", Requester: "owner", Purpose: "workstation audio prepare",
		RequestKey: taskID, Authorization: localCheckpointKey(fish, taskID, inputHash)}
	policy := &publicAudioPreparePolicy{host: host, installed: installed, configRevision: configuration.Revision, invocation: invocation,
		input: inputPath, output: outputPath, inputHash: strings.ToLower(inputHash), taskID: taskID, request: request}
	if err := controller.EnsureStarted(ctx, localAudioPackage, installed.Revision); err != nil {
		return empty, errors.New("音频工坊隔离运行器未能启动")
	}
	args, _ := json.Marshal(map[string]any{"artifact": map[string]string{"artifact_id": taskID, "sha256": strings.ToLower(inputHash)},
		"filename": request.Filename, "start_seconds": request.Start, "duration_seconds": request.Duration, "channels": request.Channels})
	result, err := controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: localAudioPackage, Capability: localAudioCapability, Context: invocation, Arguments: args}, policy)
	if err != nil {
		return empty, err
	}
	if result.Job.State != "completed" || len(policy.result.WAV) == 0 {
		return empty, errors.New("音频工坊结果未确认，请核对原任务")
	}
	if err := policy.Check(ctx, result.Job, pluginhost.Capability{ID: localAudioCapability}); err != nil {
		return empty, err
	}
	return policy.result, nil
}
