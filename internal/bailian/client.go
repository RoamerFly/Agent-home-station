// Package bailian connects only to administrator-configured Alibaba endpoints.
package bailian

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	YuEEnabled   bool   `json:"yue_enabled"`
	YuEEndpoint  string `json:"yue_endpoint"`
	YuEToken     string `json:"yue_token,omitempty"`
	Endpoint     string `json:"endpoint"`
	Key          string `json:"key,omitempty"`
	TTSModel     string `json:"tts_model"`
	ASRModel     string `json:"asr_model"`
	Voice        string `json:"voice"`
	Instruction  string `json:"instruction"`
	TTSEnabled   bool   `json:"tts_enabled"`
	ASREnabled   bool   `json:"asr_enabled"`
	MusicEnabled bool   `json:"music_enabled"`
	MusicModel   string `json:"music_model"`
}

func Defaults() Config {
	return Config{Endpoint: "https://dashscope.aliyuncs.com", TTSModel: "qwen-audio-3.1-tts-plus", ASRModel: "qwen-audio-3.1-asr-flash", Voice: "longanlingxin_v3.1", Instruction: "年轻活泼、轻松俏皮的中文女声，语气自然，不过度夸张。", MusicModel: "fun-music-v1"}
}
func (c Config) Validate() error {
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

type Client struct {
	Config Config
	HTTP   *http.Client
}

func New(c Config) *Client {
	return &Client{Config: c, HTTP: &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}}
}

type Failure struct {
	Status int
	Code   string
}

func (e *Failure) Error() string {
	return "百炼调用失败：" + e.Code + "（请检查地域、模型权限或额度；不会自动重复生成）"
}
func (c *Client) request(ctx context.Context, path string, v any) (map[string]any, error) {
	if c.Config.Key == "" {
		return nil, errors.New("百炼密钥尚未配置")
	}
	b, _ := json.Marshal(v)
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Config.Endpoint, "/")+path, bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+c.Config.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-DashScope-SSE", "disable")
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return nil, errors.New("百炼连接失败或等待超时")
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil || len(data) > 2<<20 {
		return nil, errors.New("百炼响应超过限制")
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return nil, errors.New("百炼响应无效")
	}
	if resp.StatusCode != 200 {
		code, _ := out["code"].(string)
		if len(code) > 100 || strings.ContainsAny(code, "\r\n ") {
			code = "provider_rejected"
		}
		return nil, &Failure{resp.StatusCode, code}
	}
	return out, nil
}
func (c *Client) Synthesize(ctx context.Context, text, instruction string) (string, error) {
	if strings.TrimSpace(text) == "" || len([]rune(text)) > 2000 {
		return "", errors.New("合成文字须为1–2000字符")
	}
	if instruction == "" {
		instruction = c.Config.Instruction
	}
	out, e := c.request(ctx, "/api/v1/services/audio/tts/SpeechSynthesizer", map[string]any{"model": c.Config.TTSModel, "input": map[string]any{"text": text, "voice": c.Config.Voice, "format": "wav", "sample_rate": 24000, "instruction": instruction}})
	if e != nil {
		return "", e
	}
	return audioURL(out)
}
func audioURL(out map[string]any) (string, error) {
	output, _ := out["output"].(map[string]any)
	audio, _ := output["audio"].(map[string]any)
	u, _ := audio["url"].(string)
	parsed, e := url.Parse(u)
	if e != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("百炼未返回完整音频")
	}
	return u, nil
}
func (c *Client) Transcribe(ctx context.Context, data []byte) (string, error) {
	if len(data) == 0 || len(data) > 10<<20 {
		return "", errors.New("语音识别文件须小于10MB")
	}
	out, e := c.request(ctx, "/api/v1/services/aigc/multimodal-generation/generation", map[string]any{"model": c.Config.ASRModel, "input": map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(data)}}}}}}, "parameters": map[string]string{"format": "wav", "sample_rate": "16000"}})
	if e != nil {
		return "", e
	}
	output, _ := out["output"].(map[string]any)
	text, _ := output["text"].(string)
	if text == "" {
		inner, _ := output["output"].(map[string]any)
		text, _ = inner["text"].(string)
	}
	if strings.TrimSpace(text) == "" || len(text) > 16000 {
		return "", errors.New("语音中未取得有效转写文字")
	}
	return text, nil
}
func (c *Client) Music(ctx context.Context, prompt, lyrics string) (string, error) {
	if len([]rune(prompt)) > 2000 || len([]rune(lyrics)) > 2000 || strings.TrimSpace(prompt+lyrics) == "" {
		return "", errors.New("歌曲提示词或歌词无效")
	}
	input := map[string]any{"prompt": prompt, "gender": "female", "format": "mp3"}
	if lyrics != "" {
		input["lyrics"] = lyrics
	}
	out, e := c.request(ctx, "/api/v1/services/audio/music/generation", map[string]any{"model": c.Config.MusicModel, "input": input})
	if e != nil {
		return "", e
	}
	return audioURL(out)
}
