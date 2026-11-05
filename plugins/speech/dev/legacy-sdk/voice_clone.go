package legacysdk

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
)

type CloneOptions struct {
	Preprocess      bool
	NormalizeVolume bool
}

// CloneVoice enrolls a local PCM WAV without publishing a reference URL.
// Save the returned ID before querying readiness; never retry creation blindly.
func (c *Client) CloneVoice(ctx context.Context, prefix string, wav []byte, options CloneOptions) (DesignedVoice, error) {
	var voice DesignedVoice
	if !voicePrefix.MatchString(prefix) || len(wav) < 44 || len(wav) > 10<<20 {
		return voice, errors.New("复刻前缀无效或参考音频超过限制")
	}
	// Accept a canonical PCM WAV prepared locally; reject ambiguous containers.
	if string(wav[:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(wav[16:20]) != 16 || binary.LittleEndian.Uint16(wav[20:22]) != 1 || string(wav[36:40]) != "data" {
		return voice, errors.New("参考音频须先转换为标准16位PCM WAV")
	}
	channels, rate := binary.LittleEndian.Uint16(wav[22:24]), binary.LittleEndian.Uint32(wav[24:28])
	align := binary.LittleEndian.Uint16(wav[32:34])
	if channels < 1 || channels > 2 || rate < 16000 || rate > 192000 || binary.LittleEndian.Uint16(wav[34:36]) != 16 || align != channels*2 || binary.LittleEndian.Uint32(wav[28:32]) != rate*uint32(align) || binary.LittleEndian.Uint32(wav[4:8]) != uint32(len(wav)-8) || binary.LittleEndian.Uint32(wav[40:44]) != uint32(len(wav)-44) || (len(wav)-44)%int(align) != 0 {
		return voice, errors.New("参考PCM音频格式或容器长度不完整")
	}
	duration := float64(len(wav)-44) / float64(rate*uint32(align))
	if duration < 5 || duration > 60 {
		return voice, errors.New("参考语音须为5至60秒，建议10至20秒")
	}
	out, err := c.request(ctx, "/api/v1/services/audio/tts/customization", map[string]any{"model": "voice-enrollment", "input": map[string]any{
		"action": "create_voice", "target_model": c.Config.TTSModel, "prefix": prefix,
		"audio":          "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav),
		"language_hints": []string{"zh"}, "max_prompt_audio_length": 30.0,
		"enable_preprocess": options.Preprocess, "enable_volume_normalization": strconv.FormatBool(options.NormalizeVolume),
	}})
	if err != nil {
		return voice, err
	}
	output, _ := out["output"].(map[string]any)
	voice.ID, _ = output["voice_id"].(string)
	voice.Model, _ = output["target_model"].(string)
	if voice.ID == "" || len(voice.ID) > 128 || strings.ContainsAny(voice.ID, "\r\n\x00") || voice.Model != "" && voice.Model != c.Config.TTSModel {
		return voice, errors.New("复刻响应缺失音色编号或模型不符；请查询原创建记录，不自动重复创建")
	}
	return voice, nil
}
