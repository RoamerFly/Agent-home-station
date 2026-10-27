package bailian

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const AudioReviewModel = "qwen3.8-omni-flash"

// ReviewAudio observes sound rather than trying to infer timbre from ASR text.
// It is a model opinion about the supplied samples, not human listening proof.
func (c *Client) ReviewAudio(ctx context.Context, wav []byte, question string) (string, error) {
	if c.Config.Key == "" || len(wav) < 44 || len(wav) > 3<<20 {
		return "", errors.New("音频听感检查缺少密钥或有效片段")
	}
	body := map[string]any{"model": AudioReviewModel, "stream": true, "enable_thinking": false, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": question}, map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav), "format": "wav"}}}}}}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Config.Endpoint, "/")+"/compatible-mode/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+c.Config.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", errors.New("音频听感检查连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", &Failure{Status: resp.StatusCode, Code: "audio_review_provider_rejected"}
	}
	reader := io.LimitReader(resp.Body, (2<<20)+1)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	var text strings.Builder
	finished := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if raw == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(raw), &chunk) != nil {
			return "", errors.New("音频听感检查流格式无效")
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Delta.Content)
			if choice.FinishReason != nil && *choice.FinishReason == "stop" {
				finished = true
			}
		}
		if text.Len() > 16000 {
			return "", errors.New("音频听感检查响应过长")
		}
	}
	if scanner.Err() != nil || !finished || strings.TrimSpace(text.String()) == "" {
		return "", errors.New("音频听感检查响应未完成")
	}
	return text.String(), nil
}
