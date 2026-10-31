package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"wechat-bot/internal/bailian"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

type mediaAudioReference struct {
	Ref     string `json:"media_ref"`
	SHA     string `json:"sha256"`
	Version int    `json:"version"`
	MIME    string `json:"mime"`
}
type mediaTranscription struct {
	Text    string                `json:"text"`
	Status  string                `json:"status"`
	Sources []mediaAudioReference `json:"sources"`
}

// mediaTranscriptionPolicy is deliberately separate from the visual policy:
// the provider protocol is non-chat and audio may never be substituted for an image.
type mediaTranscriptionPolicy struct {
	*receivedSharePolicy
	mu             sync.Mutex
	wav            []byte
	ref            mediaAudioReference
	model          string
	configRevision int
	requested      bool
	providerReply  json.RawMessage
	post           pluginruntime.RouteTransport
	arguments      json.RawMessage
}

func (m *Manager) newMediaTranscriptionPolicy(ctx context.Context, message linuxnative.Message, wav []byte) (*mediaTranscriptionPolicy, error) {
	base, err := m.newReceivedSharePolicyFor(ctx, message, "media.transcribe")
	if err != nil {
		return nil, err
	}
	if len(wav) == 0 || len(wav) > 10<<20 || !bytes.HasPrefix(wav, []byte("RIFF")) || len(wav) < 12 || string(wav[8:12]) != "WAVE" {
		return nil, errors.New("media_audio_format_invalid")
	}
	cfg, err := base.store.Configuration(ctx, "media-understanding")
	if err != nil {
		return nil, err
	}
	var model string
	if json.Unmarshal(cfg.Values["asr_model"], &model) != nil || model == "" {
		return nil, errors.New("media_transcription_configuration_invalid")
	}
	copyWAV := append([]byte(nil), wav...)
	sum := sha256.Sum256(copyWAV)
	p := &mediaTranscriptionPolicy{receivedSharePolicy: base, wav: copyWAV, ref: mediaAudioReference{Ref: "audio_0", SHA: hex.EncodeToString(sum[:]), Version: base.source.Revision, MIME: "audio/wav"}, model: model, configRevision: cfg.Revision, post: mcptools.PostPluginModel}
	p.arguments, _ = json.Marshal(map[string]any{"audio": p.ref, "timeout_seconds": 180})
	base.requestSuffix = agentID(string(p.arguments), strconv.Itoa(cfg.Revision))
	return p, nil
}
func (p *mediaTranscriptionPolicy) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	if err := p.receivedSharePolicy.Check(ctx, j, cap); err != nil {
		return err
	}
	cfg, err := p.store.Configuration(ctx, j.Plugin)
	if err != nil || cfg.Revision != p.configRevision {
		return errors.New("media_plugin_configuration_changed")
	}
	if j.PackageDigest != "" {
		i, err := p.store.Installed(ctx, j.Plugin)
		if err != nil || i.Digest != j.PackageDigest || i.Package.Version != j.Version || j.ConfigRevision != strconv.Itoa(p.configRevision) {
			return errors.New("media_plugin_version_changed")
		}
		if pluginhost.Authorize(i.Package, pluginhost.Grant{Account: p.message.AccountID, Plugin: i.Package.ID, Version: i.Package.Version, Permissions: i.Permissions}, j.Context, i.Package.Permissions) != nil {
			return errors.New("media_plugin_permissions_changed")
		}
	}
	if j.ID != "" {
		current, err := p.store.Job(ctx, j.ID)
		if err != nil || current.CancelRequested || current.Revision != j.Revision {
			return errors.New("media_plugin_task_changed")
		}
	}
	return nil
}
func (p *mediaTranscriptionPolicy) materialize(_ context.Context, route string, raw json.RawMessage) (json.RawMessage, error) {
	var body struct {
		Model string `json:"model"`
		Input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type  string `json:"type"`
					Audio *struct {
						Data struct {
							Ref mediaAudioReference `json:"$media_ref"`
						} `json:"data"`
					} `json:"input_audio"`
				} `json:"content"`
			} `json:"messages"`
		} `json:"input"`
		Parameters struct {
			Format string `json:"format"`
			Rate   string `json:"sample_rate"`
		} `json:"parameters"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || route != "transcription" || body.Model != p.model || len(body.Input.Messages) != 1 || body.Input.Messages[0].Role != "user" || len(body.Input.Messages[0].Content) != 1 || body.Input.Messages[0].Content[0].Type != "input_audio" || body.Input.Messages[0].Content[0].Audio == nil || body.Input.Messages[0].Content[0].Audio.Data.Ref != p.ref || body.Parameters.Format != "wav" || body.Parameters.Rate != "16000" {
		return nil, errors.New("media_transcription_request_changed")
	}
	content := map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(p.wav)}}
	return json.Marshal(map[string]any{"model": p.model, "input": map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{content}}}}, "parameters": map[string]string{"format": "wav", "sample_rate": "16000"}})
}
func (p *mediaTranscriptionPolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if method != "wechat/model_request" {
		return nil, errors.New("media_host_service_unavailable")
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	if p.requested {
		return nil, errors.New("media_model_already_requested")
	}
	return pluginruntime.ProxyModelRoute(ctx, p.store, j, cap, raw, p.materialize, func(ctx context.Context, endpoint string, headers map[string]string, body json.RawMessage, limit int64, timeout time.Duration) (json.RawMessage, error) {
		if err := p.Check(ctx, j, cap); err != nil {
			return nil, err
		}
		p.requested = true
		reply, err := p.post(ctx, endpoint, headers, body, limit, timeout)
		// The ASR protocol has no verified token schema. This records provider
		// counts only if they exist; otherwise the attempt remains unknown.
		usage := deepseek.ReportedUsage(reply, time.Now().UTC())
		usage.Model = p.model
		usage.Purpose = "plugin_media_transcription"
		c := Config{Provider: "bailian", BaseURL: strings.TrimSuffix(endpoint, "/api/v1/services/aigc/multimodal-generation/generation"), Model: p.model, APIKey: strings.TrimPrefix(headers["Authorization"], "Bearer ")}
		if p.manager.billing == nil || p.manager.billing.record(c, usage) != nil {
			return nil, errors.New("media_usage_record_failed")
		}
		if err != nil {
			return nil, err
		}
		p.providerReply = append(json.RawMessage(nil), reply...)
		return reply, nil
	})
}
func (p *mediaTranscriptionPolicy) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, j, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if _, err := decodeShareJSON(raw); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	var failure struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&failure) == nil && failure.OK != nil && !*failure.OK && failure.Error == "asr_provider_failed" && p.requested && len(p.providerReply) != 0 {
		var providerCode struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(p.providerReply, &providerCode) == nil && providerCode.Code != "" && providerCode.Code != "ASR_RESPONSE_HAVE_NO_WORDS" {
			return pluginruntime.VerifiedResult{Failed: true, Result: raw}, nil
		}
	}
	var result struct {
		OK bool `json:"ok"`
		mediaTranscription
		Coverage  string `json:"coverage"`
		Untrusted bool   `json:"untrusted_external_content"`
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || !result.OK || !result.Untrusted || result.Coverage != "provided_audio_only" || !p.requested || len(p.providerReply) == 0 || len(result.Sources) != 1 || result.Sources[0] != p.ref {
		return pluginruntime.VerifiedResult{}, errors.New("media_transcription_unverified")
	}
	var provider struct {
		Code   string `json:"code"`
		Output struct {
			Text   string `json:"text"`
			Nested struct {
				Text string `json:"text"`
			} `json:"output"`
		} `json:"output"`
	}
	if _, err := decodeShareJSON(p.providerReply); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if json.Unmarshal(p.providerReply, &provider) != nil {
		return pluginruntime.VerifiedResult{}, errors.New("media_provider_response_invalid")
	}
	text := strings.TrimSpace(provider.Output.Text)
	if text == "" {
		text = strings.TrimSpace(provider.Output.Nested.Text)
	}
	if provider.Code == "ASR_RESPONSE_HAVE_NO_WORDS" {
		if result.Status != "no_recognizable_speech" || result.Text != "" {
			return pluginruntime.VerifiedResult{}, errors.New("media_transcription_changed")
		}
	} else if provider.Code != "" || result.Status != "transcribed" || result.Text == "" || result.Text != text {
		return pluginruntime.VerifiedResult{}, errors.New("media_transcription_changed")
	}
	private, _ := json.Marshal(result.mediaTranscription)
	public, _ := json.Marshal(map[string]any{"ok": true, "status": result.Status, "coverage": result.Coverage, "sources": []mediaAudioReference{p.ref}, "usage_source": "host_transport"})
	return pluginruntime.VerifiedResult{Result: public, Private: private}, nil
}
func (p *mediaTranscriptionPolicy) transcribe(ctx context.Context) (string, error) {
	j := pluginhost.Job{Plugin: "media-understanding", Tool: "media.transcribe", Context: p.invocation()}
	if err := p.Check(ctx, j, pluginhost.Capability{}); err != nil {
		return "", err
	}
	controller, ready, err := p.manager.installedPluginRuntime()
	if err != nil || !ready {
		return "", errors.New("media_plugin_runtime_unavailable")
	}
	installed, err := p.store.Installed(ctx, j.Plugin)
	if err != nil {
		return "", err
	}
	if controller.Status(j.Plugin).State == "stopped" {
		if err = controller.Start(ctx, j.Plugin, installed.Revision); err != nil {
			return "", errors.New("media_plugin_start_failed")
		}
	}
	call, err := controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: j.Plugin, Capability: j.Tool, Context: j.Context, Arguments: p.arguments}, p)
	if err != nil {
		return "", errors.New("media_plugin_transcription_needs_review")
	}
	if call.Job.State != "completed" {
		return "", errors.New("media_plugin_transcription_needs_review")
	}
	raw, err := p.store.PrivateJobResult(ctx, call.Job, func(ctx context.Context, j pluginhost.Job) error { return p.Check(ctx, j, pluginhost.Capability{}) })
	if err != nil {
		return "", errors.New("media_plugin_result_unavailable")
	}
	var r mediaTranscription
	if json.Unmarshal(raw, &r) != nil {
		return "", errors.New("media_plugin_result_invalid")
	}
	if r.Status == "no_recognizable_speech" {
		return "", &bailian.Failure{Code: "ASR_RESPONSE_HAVE_NO_WORDS"}
	}
	if r.Status != "transcribed" || r.Text == "" {
		return "", errors.New("media_plugin_result_invalid")
	}
	return r.Text, nil
}
