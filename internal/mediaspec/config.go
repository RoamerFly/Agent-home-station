// Package mediaspec defines legacy media configuration and observation contracts.
// It performs no provider requests, synthesis, transcription or model execution.
package mediaspec

import (
	"errors"
	"net/url"
	"strings"
)

type Config struct {
	YuEEnabled    bool   `json:"yue_enabled"`
	YuEEndpoint   string `json:"yue_endpoint"`
	YuEToken      string `json:"yue_token,omitempty"`
	Endpoint      string `json:"endpoint"`
	Key           string `json:"key,omitempty"`
	TTSModel      string `json:"tts_model"`
	ASRModel      string `json:"asr_model"`
	ASRBackend    string `json:"asr_backend,omitempty"`
	ASRLocalModel string `json:"asr_local_model,omitempty"`
	Voice         string `json:"voice"`
	Instruction   string `json:"instruction"`
	TTSEnabled    bool   `json:"tts_enabled"`
	ASREnabled    bool   `json:"asr_enabled"`
	MusicEnabled  bool   `json:"music_enabled"`
	MusicModel    string `json:"music_model"`
}

func Defaults() Config {
	return Config{Endpoint: "https://dashscope.aliyuncs.com", TTSModel: "qwen-audio-3.1-tts-plus", ASRModel: "qwen-audio-3.1-asr-flash", Voice: "longanlingxin_v3.1", Instruction: "年轻活泼、轻松俏皮的中文女声，语气自然，不过度夸张。", MusicModel: "fun-music-v1"}
}
func (c Config) Validate() error {
	if c.ASRBackend != "" && c.ASRBackend != "bailian" && c.ASRBackend != "local" && c.ASRBackend != "local_then_bailian" {
		return errors.New("语音识别后端无效")
	}
	if strings.ContainsAny(c.ASRLocalModel, "/\\\x00\r\n") || len(c.ASRLocalModel) > 100 || c.ASRLocalModel == ".." {
		return errors.New("本地识别模型需为已登记的模型名称")
	}
	if c.YuEEndpoint != "" {
		u, e := url.Parse(c.YuEEndpoint)
		if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || strings.Trim(u.Path, "/") != "" {
			return errors.New("本地YuE地址无效")
		}
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return errors.New("百炼地址必须是HTTPS地域根地址")
	}
	host := u.Hostname()
	if host != "dashscope.aliyuncs.com" && host != "dashscope-intl.aliyuncs.com" && !strings.HasSuffix(host, ".maas.aliyuncs.com") {
		return errors.New("百炼地址必须是官方地域或业务空间域名")
	}
	for _, s := range []string{c.TTSModel, c.ASRModel, c.Voice, c.MusicModel} {
		if s == "" || len(s) > 128 || strings.ContainsAny(s, "\r\n\x00") {
			return errors.New("百炼模型或音色无效")
		}
	}
	if len(c.Instruction) > 1500 || len(c.Key) > 4096 || strings.ContainsAny(c.Key, " \r\n\x00") {
		return errors.New("百炼参数无效")
	}
	return nil
}
