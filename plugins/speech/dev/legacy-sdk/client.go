// Package legacysdk retains explicit developer-only provider auditions.
// The bot core must not import this package; it declares no runtime capability.
package legacysdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"wechat-bot/internal/mediaspec"
)

type Config = mediaspec.Config
type Failure = mediaspec.Failure

func Defaults() Config          { return mediaspec.Defaults() }
func IsNoSpeech(err error) bool { return mediaspec.IsNoSpeech(err) }

type Client struct {
	Config Config
	HTTP   *http.Client
}

func New(c Config) *Client {
	return &Client{Config: c, HTTP: &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}}
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
		message, _ := out["message"].(string)
		if code == "CLIENT_ERROR" && strings.TrimSpace(message) == "ASR_RESPONSE_HAVE_NO_WORDS" {
			code = "ASR_RESPONSE_HAVE_NO_WORDS"
		}
		if len(code) > 100 || strings.ContainsAny(code, "\r\n ") {
			code = "provider_rejected"
		}
		return nil, &Failure{Status: resp.StatusCode, Code: code}
	}
	return out, nil
}
