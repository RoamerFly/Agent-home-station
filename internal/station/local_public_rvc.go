package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
)

type publicRVCSubmission struct {
	Binding, VoiceID, Title, InputHash, RemoteID string
	Seconds                                      float64
	Pin                                          localRVCVoicePin
}

type publicRVCProduct struct {
	WAV      []byte
	SHA256   string
	RemoteID string
}

// publicRVCVoices reads only the workstation's installed capability and
// registered voice metadata. No application source or reply target enters it.
func (s *localProjectTools) publicRVCVoices(ctx context.Context, fish string) ([]localRVCVoice, error) {
	if s == nil || s.rvc == nil || !s.rvcRegistered(fish) {
		return nil, errors.New("工作站 RVC 未登记")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return nil, err
	}
	if _, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localRVCPackage, capability: localRVCCapability}); err != nil {
		return nil, err
	}
	info, err := s.rvc.Info(ctx, fish)
	if err != nil || len(info.Profiles) == 0 {
		return nil, errors.New("工作站 RVC 声线当前不可用")
	}
	return info.Profiles, nil
}

// submitPublicRVC receives an immutable WAV and an opaque application job ID.
// The application keeps the message source, permissions and delivery receipt.
func (s *localProjectTools) submitPublicRVC(ctx context.Context, fish, jobID, voiceID, title, inputHash string, wav []byte) (publicRVCSubmission, error) {
	var submission publicRVCSubmission
	if !validLocalRVCHex(jobID, 64) || !validLocalRVCHex(inputHash, 64) || len(wav) > localRVCInputLimit {
		return submission, errors.New("RVC 请求编号、原件摘要或长度无效")
	}
	sum := sha256.Sum256(wav)
	evidence, err := inspectRVCWAV(wav)
	if err != nil || hex.EncodeToString(sum[:]) != inputHash || evidence.seconds() > 600 {
		return submission, errors.New("RVC 输入须为完整且不超过 600 秒的 PCM16 WAV")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "本机 RVC 换声"
	}
	if !safeRVCDisplay(title, 100) {
		return submission, errors.New("RVC 标题无效")
	}
	s.update.Lock()
	defer s.update.Unlock()
	voices, err := s.publicRVCVoices(ctx, fish)
	if err != nil {
		return submission, err
	}
	selected := ""
	for _, voice := range voices {
		if voice.ID == voiceID || voice.LegacyVoiceID != "" && voice.LegacyVoiceID == voiceID {
			selected = voice.ID
			break
		}
	}
	if selected == "" {
		return submission, errors.New("指定 RVC 声线未登记")
	}
	deployment, err := s.rvc.Info(ctx, fish)
	if err != nil || deployment.Binding == "" {
		return submission, errors.New("工作站 RVC 部署不可用")
	}
	data, code, err := s.rvc.Call(ctx, fish, deployment.Binding, http.MethodGet, "/rvc-health?voice_profile_id="+url.QueryEscape(selected), "", nil)
	var health struct {
		localRVCVoicePin
		Ready   bool   `json:"ready"`
		Engine  string `json:"engine"`
		VoiceID string `json:"voice_profile_id"`
	}
	if err != nil || code != http.StatusOK || json.Unmarshal(data, &health) != nil || !health.Ready ||
		health.Engine != "rvc" || health.VoiceID != selected || !health.localRVCVoicePin.valid() {
		return submission, errors.New("指定 RVC 声线未就绪")
	}
	submission = publicRVCSubmission{Binding: deployment.Binding, VoiceID: selected, Title: title,
		InputHash: inputHash, Seconds: evidence.seconds(), Pin: health.localRVCVoicePin}
	payload, err := json.Marshal(map[string]any{"title": title, "voice_profile_id": selected, "reference_base64": wav,
		"reference_start_seconds": 0, "reference_seconds": submission.Seconds, "reference_pretrimmed": true,
		"reference_source_sha256": inputHash, "profile_config_sha256": submission.Pin.ConfigHash})
	if err != nil {
		return submission, err
	}
	data, code, err = s.rvc.Call(ctx, fish, deployment.Binding, http.MethodPost, "/rvc-jobs", jobID, payload)
	if err != nil {
		return submission, err
	}
	var remote localRVCRemoteJob
	if json.Unmarshal(data, &remote) != nil || !validLocalRVCHex(remote.ID, 32) || remote.Engine != "rvc" ||
		(code != http.StatusAccepted && code != http.StatusOK) ||
		(code == http.StatusAccepted && (remote.State != "queued" || remote.VoiceID != selected)) ||
		(code == http.StatusOK && !remote.matches(submission.rvcInput())) {
		return submission, errors.New("RVC 提交回执不可信，须查询原任务")
	}
	submission.RemoteID = remote.ID
	return submission, nil
}

func (j publicRVCSubmission) rvcInput() localRVCInput {
	return localRVCInput{Artifact: localArtifactLink{SHA256: j.InputHash}, VoiceID: j.VoiceID,
		Title: j.Title, Seconds: j.Seconds, Pin: j.Pin}
}

// A status lookup never resubmits a conversion whose first response was lost.
func (s *localProjectTools) publicRVCStatus(ctx context.Context, fish, jobID string, submission publicRVCSubmission) (string, publicRVCProduct, error) {
	var product publicRVCProduct
	if !validLocalRVCHex(jobID, 64) || !submission.Pin.valid() || !validLocalRVCHex(submission.InputHash, 64) || submission.Binding == "" {
		return "", product, errors.New("RVC 原任务记录无效")
	}
	if _, err := s.publicRVCVoices(ctx, fish); err != nil {
		return "", product, err
	}
	deployment, err := s.rvc.Info(ctx, fish)
	if err != nil || deployment.Binding != submission.Binding {
		return "", product, errLocalRVCFollowupDeployment
	}
	route := "/rvc-requests/" + jobID
	if submission.RemoteID != "" {
		if !validLocalRVCHex(submission.RemoteID, 32) {
			return "", product, errors.New("RVC 远端任务编号无效")
		}
		route = "/jobs/" + submission.RemoteID
	}
	data, code, err := s.rvc.Call(ctx, fish, submission.Binding, http.MethodGet, route, "", nil)
	if err != nil {
		return "", product, err
	}
	if code == http.StatusNotFound {
		return "needs_review", product, nil
	}
	var remote localRVCRemoteJob
	if code != http.StatusOK || json.Unmarshal(data, &remote) != nil || !remote.matches(submission.rvcInput()) ||
		(submission.RemoteID != "" && remote.ID != submission.RemoteID) {
		return "", product, errors.New("RVC 回执与原任务不一致")
	}
	product.RemoteID = remote.ID
	if remote.State != "completed" {
		return remote.State, product, nil
	}
	if remote.Label != submission.Pin.Label || remote.ModelHash != submission.Pin.ModelHash ||
		remote.IndexHash != submission.Pin.IndexHash || remote.Device != submission.Pin.Device ||
		(remote.ActualDevice != "cpu" && remote.ActualDevice != "cuda:0") ||
		(submission.Pin.Device != "auto" && remote.ActualDevice != submission.Pin.Device) ||
		!validLocalRVCHex(remote.Hash, 64) || remote.Bytes < 44 || remote.Bytes > 128<<20 {
		return "", product, errors.New("RVC 成品声线、设备或摘要与任务不符")
	}
	data, code, err = s.rvc.Call(ctx, fish, submission.Binding, http.MethodGet, "/jobs/"+remote.ID+"/audio", "", nil)
	if err != nil || code != http.StatusOK {
		return "", product, errors.New("RVC 成品尚未取得")
	}
	sum := sha256.Sum256(data)
	wave, err := inspectRVCWAV(data)
	if err != nil || int64(len(data)) != remote.Bytes || hex.EncodeToString(sum[:]) != remote.Hash ||
		wave.Rate != remote.SampleRate || wave.Frames != remote.Frames || wave.Channels != remote.Channels ||
		remote.Bits != 16 || math.Abs(wave.seconds()-remote.Duration) > 1e-6 ||
		math.Abs(wave.seconds()-submission.Seconds) > .25 {
		return "", product, errors.New("RVC 成品 WAV 与回执不符")
	}
	return "completed", publicRVCProduct{WAV: data, SHA256: remote.Hash, RemoteID: remote.ID}, nil
}

func (s *localProjectTools) cancelPublicRVC(ctx context.Context, fish, jobID string, submission publicRVCSubmission) (string, error) {
	if !validLocalRVCHex(jobID, 64) || !validLocalRVCHex(submission.RemoteID, 32) || strings.TrimSpace(submission.Binding) == "" {
		return "", errors.New("RVC 原任务无法取消")
	}
	if _, err := s.publicRVCVoices(ctx, fish); err != nil {
		return "", err
	}
	deployment, err := s.rvc.Info(ctx, fish)
	if err != nil || deployment.Binding != submission.Binding {
		return "", errLocalRVCFollowupDeployment
	}
	data, code, err := s.rvc.Call(ctx, fish, submission.Binding, http.MethodPost, "/jobs/"+submission.RemoteID+"/cancel", "", []byte("{}"))
	var remote localRVCRemoteJob
	if err != nil || code != http.StatusOK || json.Unmarshal(data, &remote) != nil || remote.ID != submission.RemoteID || !remote.matches(submission.rvcInput()) {
		return "", errors.New("RVC 取消结果未确认")
	}
	return remote.State, nil
}

func (s *localProjectTools) resumePublicRVC(ctx context.Context, fish, jobID string, submission publicRVCSubmission) (string, error) {
	if !validLocalRVCHex(jobID, 64) || !validLocalRVCHex(submission.RemoteID, 32) || !submission.Pin.valid() {
		return "", errors.New("RVC 原任务无法续办")
	}
	voices, err := s.publicRVCVoices(ctx, fish)
	if err != nil {
		return "", err
	}
	registered := false
	for _, voice := range voices {
		registered = registered || voice.ID == submission.VoiceID
	}
	deployment, err := s.rvc.Info(ctx, fish)
	if err != nil || deployment.Binding != submission.Binding || !registered {
		return "", errLocalRVCFollowupDeployment
	}
	data, code, err := s.rvc.Call(ctx, fish, submission.Binding, http.MethodGet, "/rvc-health?voice_profile_id="+url.QueryEscape(submission.VoiceID), "", nil)
	var health struct {
		localRVCVoicePin
		Ready bool `json:"ready"`
	}
	if err != nil || code != http.StatusOK || json.Unmarshal(data, &health) != nil || !health.Ready || health.localRVCVoicePin != submission.Pin {
		return "", errors.New("RVC 原声线版本已变化")
	}
	data, code, err = s.rvc.Call(ctx, fish, submission.Binding, http.MethodPost, "/jobs/"+submission.RemoteID+"/resume", "", []byte("{}"))
	var remote localRVCRemoteJob
	if err != nil || code != http.StatusOK || json.Unmarshal(data, &remote) != nil || remote.ID != submission.RemoteID || !remote.matches(submission.rvcInput()) {
		return "", errors.New("RVC 续办结果未确认，请查询原任务")
	}
	return remote.State, nil
}
