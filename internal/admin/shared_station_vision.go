package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"wechat-bot/internal/deepseek"
)

const stationVisionPath = "/api/local/project-tools/media-understanding/vision"

func (m *Manager) sharedStationVision() (*permittedTools, bool) {
	p := &permittedTools{manager: m}
	info, err := os.Stat(p.stationToolPasswordPath())
	return p, err == nil && info.Mode().IsRegular()
}

func (m *Manager) sharedVisionReady(ctx context.Context) (bool, bool) {
	p, shared := m.sharedStationVision()
	if !shared {
		return false, false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	session, err := p.stationToolLogin(ctx)
	if err != nil {
		return true, false
	}
	data, err := p.stationToolRequest(ctx, session, http.MethodGet, stationVisionPath, nil)
	var view struct {
		Ready bool `json:"ready"`
	}
	return true, err == nil && json.Unmarshal(data, &view) == nil && view.Ready
}

func (p *permittedTools) observeSharedVision(ctx context.Context, source, prompt string, images []deepseek.Image, tokens int, check func(context.Context) error) (string, error) {
	if err := check(ctx); err != nil {
		return "", err
	}
	session, err := p.stationToolLogin(ctx)
	if err != nil {
		return "", err
	}
	data, err := p.stationToolRequest(ctx, session, http.MethodPost, stationVisionPath+"/observe", map[string]any{"source": "wechat:" + source, "prompt": prompt, "images": images, "tokens": tokens})
	if err != nil {
		return "", errors.New("media_station_vision_unavailable")
	}
	if err = check(ctx); err != nil {
		return "", err
	}
	var view struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &view) != nil || strings.TrimSpace(view.Text) == "" {
		return "", errors.New("media_station_vision_invalid")
	}
	return view.Text, nil
}
