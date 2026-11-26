package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/mediatranscribe"
	"wechat-bot/internal/pluginhost"
)

const stationASRPath = "/api/local/project-tools/audio-cpp/transcribe"

type stationASR struct {
	host    *permittedTools
	session stationToolSession
	version string
}

func (m *Manager) readyStationASR(ctx context.Context) *stationASR {
	host := &permittedTools{manager: m}
	if info, err := os.Stat(host.stationToolPasswordPath()); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	session, err := host.stationToolLogin(ctx)
	if err != nil {
		return nil
	}
	data, err := host.stationToolRequest(ctx, session, http.MethodGet, stationASRPath, nil)
	var view struct {
		Ready   bool   `json:"ready"`
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(data, &view) != nil || !view.Ready || view.Version == "" {
		return nil
	}
	return &stationASR{host: host, session: session, version: view.Version}
}

func (m *Manager) transcribeNativeAudio(ctx context.Context, message linuxnative.Message, wav []byte, station *stationASR) (string, error) {
	if station == nil {
		policy, err := m.newMediaTranscriptionPolicy(ctx, message, wav)
		if err != nil {
			return "", err
		}
		return policy.transcribe(ctx)
	}
	origin, err := m.newReceivedSharePolicyFor(ctx, message, mediatranscribe.CapabilityID)
	if err != nil {
		return "", err
	}
	job := pluginhost.Job{Plugin: mediatranscribe.PluginID, Tool: mediatranscribe.CapabilityID, Context: origin.invocation()}
	return station.transcribe(ctx, wav, func(ctx context.Context) error {
		return origin.Check(ctx, job, pluginhost.Capability{})
	})
}

func (station *stationASR) transcribe(ctx context.Context, wav []byte, check func(context.Context) error) (string, error) {
	if err := check(ctx); err != nil {
		return "", err
	}
	data, err := station.host.stationToolRequest(ctx, station.session, http.MethodPost, stationASRPath, map[string]any{"version": station.version, "audio": wav})
	if err != nil {
		return "", toolInputError("公共语音转写未返回结果，请用station_tool_manage 的 status 核对 audio-cpp；本次未切换到云端")
	}
	if err := check(ctx); err != nil {
		return "", err
	}
	var result struct {
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	if json.Unmarshal(data, &result) != nil {
		return "", toolInputError("公共语音转写响应格式无效")
	}
	if result.Status == "no_recognizable_speech" {
		return "", &mediaspec.Failure{Code: "ASR_RESPONSE_HAVE_NO_WORDS"}
	}
	if result.Status != "transcribed" || strings.TrimSpace(result.Text) == "" {
		return "", toolInputError("公共语音转写未返回有效文字")
	}
	return result.Text, nil
}
