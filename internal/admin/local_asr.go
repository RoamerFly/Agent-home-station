package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/bot"
)

type localASRManifest struct {
	Python         string  `json:"python"`
	Entry          string  `json:"entry"`
	Packages       string  `json:"packages"`
	Model          string  `json:"model"`
	Tokens         string  `json:"tokens"`
	VADModel       string  `json:"vad_model"`
	VADThreshold   float64 `json:"vad_threshold"`
	Threads        int     `json:"threads"`
	Language       string  `json:"language"`
	ChunkSeconds   float64 `json:"chunk_seconds"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

func (m *Manager) transcribeAudio(ctx context.Context, wav []byte) (string, error) {
	c := m.speechConfig()
	if !c.ASREnabled {
		return "", errors.New("语音识别未启用")
	}
	if c.ASRBackend == "" || c.ASRBackend == "bailian" {
		return bailian.New(c).Transcribe(ctx, wav)
	}
	text, e := m.transcribeLocal(ctx, wav, c.ASRLocalModel)
	if e != nil && !bailian.IsNoSpeech(e) && ctx.Err() == nil && c.ASRBackend == "local_then_bailian" {
		m.record(bot.Record{Event: "asr_backend_fallback", Category: "tool", Status: "waiting", Origin: "speech", Reason: "local_asr_failed_using_configured_bailian_fallback"})
		return bailian.New(c).Transcribe(ctx, wav)
	}
	return text, e
}

func (m *Manager) transcribeLocal(ctx context.Context, wav []byte, id string) (string, error) {
	if id == "" {
		id = "sensevoice-small-int8"
	}
	if strings.ContainsAny(id, "/\\\x00") || id == ".." {
		return "", errors.New("local_asr_model_id_invalid")
	}
	public := filepath.Join(m.managedRoot(), "public_workspace")
	manifestPath := filepath.Join(public, "manifests", "asr-"+id+".json")
	resolved, e := filepath.EvalSymlinks(manifestPath)
	if e != nil || resolved != manifestPath {
		return "", errors.New("本地ASR模型尚未登记；请安装公共模型与运行环境")
	}
	data, e := os.ReadFile(manifestPath)
	var manifest localASRManifest
	if e != nil || len(data) > 64000 || json.Unmarshal(data, &manifest) != nil {
		return "", errors.New("local_asr_manifest_invalid")
	}
	for _, p := range []string{manifest.Python, manifest.Entry, manifest.Packages, manifest.Model, manifest.Tokens} {
		rel, e := filepath.Rel(public, p)
		if e != nil || !filepath.IsAbs(p) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", errors.New("local_asr_resource_outside_public_workspace")
		}
	}
	if manifest.VADModel != "" {
		rel, e := filepath.Rel(public, manifest.VADModel)
		if e != nil || !filepath.IsAbs(manifest.VADModel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", errors.New("local_vad_resource_outside_public_workspace")
		}
	}
	if manifest.Threads == 0 {
		manifest.Threads = 2
	}
	if manifest.Language == "" {
		manifest.Language = "auto"
	}
	if manifest.ChunkSeconds == 0 {
		manifest.ChunkSeconds = 30
	}
	if manifest.TimeoutSeconds == 0 {
		manifest.TimeoutSeconds = 120
	}
	if manifest.Threads < 1 || manifest.ChunkSeconds <= 0 || manifest.TimeoutSeconds < 0 || manifest.TimeoutSeconds > float64((1<<63-1)/int64(time.Second)) {
		return "", errors.New("local_asr_parameters_invalid")
	}
	private := filepath.Join(m.managedRoot(), "pri_workspace", accountDirectory(m.native.Account()), "scratch")
	if e = ensureManaged(private); e != nil {
		return "", e
	}
	folder, e := os.MkdirTemp(private, "asr-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(folder)
	input := filepath.Join(folder, "input.wav")
	if e = os.WriteFile(input, wav, 0600); e != nil {
		return "", e
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(manifest.TimeoutSeconds*float64(time.Second)))
	defer cancel()
	cfg := agenthost.Defaults()
	cfg.Mode = "full"
	cfg.OutputBytes = 64000
	cfg.Roots = []string{folder}
	cfg.Isolation = &agenthost.Isolation{ReadOnly: []string{public}}
	argv := []string{manifest.Python, manifest.Entry, "--packages", manifest.Packages, "--model", manifest.Model, "--tokens", manifest.Tokens, "--input", input, "--threads", strconv.Itoa(manifest.Threads), "--language", manifest.Language, "--chunk-seconds", strconv.FormatFloat(manifest.ChunkSeconds, 'f', -1, 64)}
	if manifest.VADModel != "" {
		threshold := manifest.VADThreshold
		if threshold == 0 {
			threshold = .5
		}
		argv = append(argv, "--vad-model", manifest.VADModel, "--vad-threshold", strconv.FormatFloat(threshold, 'f', -1, 64))
	}
	output, e := agenthost.RunIsolated(bounded, cfg, folder, argv)
	if e != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var detail struct {
			Error string `json:"error"`
			Type  string `json:"error_type"`
		}
		if json.Unmarshal(output, &detail) == nil && detail.Error == "local_asr_inference_failed" && diagnosticCode(detail.Type) != "" {
			m.record(bot.Record{Event: "local_asr_failed", Category: "error", Status: "failed", Origin: "speech", ToolStage: "local_inference", ToolCode: detail.Type, Reason: detail.Error})
		}
		return "", errors.New("本地ASR执行失败或超时；请检查已登记运行环境、模型与隔离后端")
	}
	var result struct {
		Text     string `json:"text"`
		State    string `json:"state"`
		Complete bool   `json:"range_complete"`
	}
	if json.Unmarshal(output, &result) != nil || !result.Complete {
		return "", errors.New("local_asr_response_incomplete")
	}
	if result.State == "no_recognizable_speech" && strings.TrimSpace(result.Text) == "" {
		return "", &bailian.Failure{Code: "ASR_RESPONSE_HAVE_NO_WORDS"}
	}
	if result.State != "transcribed" || strings.TrimSpace(result.Text) == "" {
		return "", errors.New("local_asr_response_invalid")
	}
	return result.Text, nil
}

func (m *Manager) localASRStatus() map[string]any {
	id := m.speechConfig().ASRLocalModel
	if id == "" {
		id = "sensevoice-small-int8"
	}
	state := map[string]any{"id": id, "available": false, "platform": runtime.GOOS, "message": "本地模型尚未安装或登记"}
	if strings.ContainsAny(id, "/\\\x00") || id == ".." {
		return state
	}
	public := filepath.Join(m.managedRoot(), "public_workspace")
	path := filepath.Join(public, "manifests", "asr-"+id+".json")
	data, e := os.ReadFile(path)
	var manifest localASRManifest
	if e != nil || json.Unmarshal(data, &manifest) != nil {
		return state
	}
	for _, p := range []string{manifest.Python, manifest.Entry, manifest.Packages, manifest.Model, manifest.Tokens} {
		rel, e := filepath.Rel(public, p)
		if e != nil || !filepath.IsAbs(p) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return state
		}
		if _, e = os.Stat(p); e != nil {
			return state
		}
	}
	info, e := os.Stat(manifest.Model)
	if e != nil {
		return state
	}
	state["available"], state["model_bytes"] = true, info.Size()
	state["message"] = "公共模型和运行环境已登记；实际识别按输入音频验证"
	return state
}
