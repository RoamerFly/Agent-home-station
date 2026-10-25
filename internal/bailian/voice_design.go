package bailian

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

type DesignedVoice struct {
	ID         string `json:"voice_id"`
	Model      string `json:"target_model"`
	Preview    []byte `json:"-"`
	PreviewURL string `json:"-"`
	Format     string `json:"response_format"`
	SampleRate int    `json:"sample_rate"`
}

var voicePrefix = regexp.MustCompile(`^[A-Za-z0-9]{1,10}$`)

// DesignVoice creates one provider-side voice. Do not automatically retry an
// uncertain creation. A returned ID must be saved even if preview parsing fails.
func (c *Client) DesignVoice(ctx context.Context, prefix, prompt, preview string) (DesignedVoice, error) {
	var voice DesignedVoice
	if !voicePrefix.MatchString(prefix) || !utf8.ValidString(prompt) || len([]rune(prompt)) < 1 || len([]rune(prompt)) > 500 || !utf8.ValidString(preview) || len([]rune(preview)) < 15 || len([]rune(preview)) > 200 {
		return voice, errors.New("音色描述或预览文字不符合百炼限制")
	}
	out, err := c.request(ctx, "/api/v1/services/audio/tts/customization", map[string]any{"model": "voice-enrollment", "input": map[string]any{"action": "create_voice", "target_model": c.Config.TTSModel, "voice_prompt": prompt, "preview_text": preview, "prefix": prefix, "language_hints": []string{"zh"}}, "parameters": map[string]any{"sample_rate": 24000, "response_format": "mp3"}})
	if err != nil {
		return voice, err
	}
	output, _ := out["output"].(map[string]any)
	voice.ID, _ = output["voice_id"].(string)
	voice.Model, _ = output["target_model"].(string)
	if voice.ID == "" || len(voice.ID) > 128 || strings.ContainsAny(voice.ID, "\r\n\x00") || voice.Model != "" && voice.Model != c.Config.TTSModel {
		return voice, errors.New("音色创建响应不完整或目标模型不匹配；请查询原创建记录，不自动重复创建")
	}
	if voice.Model == "" {
		details, err := c.InspectDesignedVoice(ctx, voice.ID)
		if err != nil {
			return voice, err
		}
		voice.Model, voice.PreviewURL = details.Model, details.PreviewURL
	}
	audio, _ := output["preview_audio"].(map[string]any)
	data, _ := audio["data"].(string)
	if data == "" {
		if voice.PreviewURL == "" {
			details, err := c.InspectDesignedVoice(ctx, voice.ID)
			if err != nil {
				return voice, err
			}
			voice.PreviewURL = details.PreviewURL
		}
		if voice.PreviewURL != "" {
			return voice, nil
		}
	}
	voice.Format, _ = audio["response_format"].(string)
	rate, _ := audio["sample_rate"].(float64)
	voice.SampleRate = int(rate)
	if len(data) > 2<<20 || voice.Format != "mp3" || voice.SampleRate != 24000 {
		// Some deployments ignore the requested preview format. Recover the
		// provider-hosted resource instead of creating another voice.
		details, inspectErr := c.InspectDesignedVoice(ctx, voice.ID)
		if inspectErr == nil && details.PreviewURL != "" {
			voice.Model, voice.PreviewURL = details.Model, details.PreviewURL
			return voice, nil
		}
		return voice, errors.New("音色已创建，但预览格式或大小异常；保留音色ID，不自动重复创建")
	}
	voice.Preview, err = base64.StdEncoding.DecodeString(data)
	if err != nil || len(voice.Preview) == 0 {
		return voice, errors.New("音色已创建，但预览音频无法解码；保留音色ID，不自动重复创建")
	}
	return voice, nil
}

// InspectDesignedVoice recovers an already-created voice without creating it
// again. Some service deployments return resource_link instead of inline audio.
func (c *Client) InspectDesignedVoice(ctx context.Context, id string) (DesignedVoice, error) {
	voice := DesignedVoice{ID: id}
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\x00") {
		return voice, errors.New("音色编号无效")
	}
	out, err := c.request(ctx, "/api/v1/services/audio/tts/customization", map[string]any{"model": "voice-enrollment", "input": map[string]string{"action": "query_voice", "voice_id": id}})
	if err != nil {
		return voice, err
	}
	output, _ := out["output"].(map[string]any)
	actual, _ := output["voice_id"].(string)
	voice.Model, _ = output["target_model"].(string)
	if actual != id || voice.Model != c.Config.TTSModel || output["status"] != "OK" {
		return voice, errors.New("自定义音色状态或绑定模型不匹配")
	}
	voice.PreviewURL, _ = output["resource_link"].(string)
	return voice, nil
}
