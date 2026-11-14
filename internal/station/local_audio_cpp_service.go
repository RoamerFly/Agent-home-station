package station

// audio.cpp is a host-owned, loopback-only adapter.  The chat model receives
// model/voice summaries and verified results; it never receives the server
// URL, executable, model path or arbitrary HTTP arguments.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/pluginruntime"
)

const (
	localAudioCppPackage          = "audio-cpp"
	localAudioCppRegistrationFile = "station-audio-cpp.json"
	localAudioCppOwner            = "audio-cpp"
	localAudioCppServiceID        = "audio-cpp"
	localAudioCppMaxConfig        = 256 << 10
	localAudioCppMaxWAV           = 32 << 20
)

var localAudioCppGrants = []string{"artifact.read", "artifact.write", "model.invoke", "gpu"}

var localAudioCppHex = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type localAudioCppEngineRegistration struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	CLI     string `json:"cli"`
	Server  string `json:"server"`
}

type localAudioCppModelRegistration struct {
	ID        string `json:"id"`
	PackageID string `json:"package_id"`
	Family    string `json:"family"`
	Task      string `json:"task"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Verified  string `json:"verified"`
	State     string `json:"state,omitempty"`
}

type localAudioCppPermissions struct {
	ModelRead        bool   `json:"model_read"`
	ArtifactWrite    bool   `json:"artifact_write"`
	Network          string `json:"network"`
	ExternalDelivery bool   `json:"external_delivery"`
}

type localAudioCppServerModel struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Task string `json:"task"`
}

type localAudioCppServerConfig struct {
	Host       string                     `json:"host"`
	Port       int                        `json:"port"`
	Backend    string                     `json:"backend"`
	ModelsRoot string                     `json:"models_root"`
	Models     []localAudioCppServerModel `json:"models"`
}

type localAudioCppRegistration struct {
	SchemaVersion int                              `json:"schema_version"`
	Enabled       bool                             `json:"enabled"`
	Runtime       string                           `json:"runtime"`
	Backend       string                           `json:"backend"`
	Host          string                           `json:"host"`
	Port          int                              `json:"port"`
	ModelsRoot    string                           `json:"models_root"`
	ServerConfig  string                           `json:"server_config"`
	Engine        localAudioCppEngineRegistration  `json:"engine"`
	Models        []localAudioCppModelRegistration `json:"models"`
	Permissions   localAudioCppPermissions         `json:"permissions"`
	TTSModelID    string                           `json:"tts_model_id"`
	ASRModelID    string                           `json:"asr_model_id"`
}

type localAudioCppModelView struct {
	ID         string `json:"id"`
	Task       string `json:"task"`
	Family     string `json:"family"`
	Configured bool   `json:"configured"`
	State      string `json:"state,omitempty"`
}

type localAudioCppDeployment struct {
	Binding    string                   `json:"binding"`
	Backend    string                   `json:"backend"`
	Version    string                   `json:"version"`
	Commit     string                   `json:"commit"`
	TTSModelID string                   `json:"tts_model_id"`
	ASRModelID string                   `json:"asr_model_id"`
	Running    bool                     `json:"running"`
	Busy       bool                     `json:"busy"`
	Models     []localAudioCppModelView `json:"models"`
}

type localAudioCppBackend interface {
	Info(context.Context, string) (localAudioCppDeployment, error)
	Start(context.Context, string) error
	Stop(context.Context) error
	Close(context.Context) error
	Synthesize(context.Context, string, string, localTTSRequest) (localTTSResult, error)
	Transcribe(context.Context, string, string, []byte) (string, error)
}

type localAudioCppService struct {
	dataDir      string
	editableRoot string
	services     pluginruntime.ManagedServices
	mu           sync.Mutex
	closed       bool
}

func newLocalAudioCppService(dataDir, editableRoot string) localAudioCppBackend {
	return &localAudioCppService{dataDir: filepath.Clean(dataDir), editableRoot: filepath.Clean(editableRoot)}
}

func (s *localAudioCppService) Info(ctx context.Context, fish string) (localAudioCppDeployment, error) {
	if err := ctx.Err(); err != nil {
		return localAudioCppDeployment{}, err
	}
	if !validLocalAudioCppFish(fish) {
		return localAudioCppDeployment{}, errors.New("audio_cpp_owner_invalid")
	}
	r, raw, err := s.loadRegistration(ctx)
	if err != nil {
		return localAudioCppDeployment{}, err
	}
	models := make([]localAudioCppModelView, 0, len(r.Models))
	for _, m := range r.Models {
		_, statErr := os.Stat(m.Path)
		models = append(models, localAudioCppModelView{ID: m.ID, Task: m.Task, Family: m.Family, Configured: statErr == nil, State: m.State})
	}
	status := s.services.Status(localAudioCppServiceIDForFish(fish))
	return localAudioCppDeployment{Binding: localAudioCppBinding(raw), Backend: r.Backend, Version: r.Engine.Version, Commit: r.Engine.Commit, TTSModelID: r.TTSModelID, ASRModelID: r.ASRModelID, Running: status.Running, Busy: status.Busy, Models: models}, nil
}

func (s *localAudioCppService) Start(ctx context.Context, fish string) error {
	if runtime.GOOS != "windows" {
		return errors.New("audio_cpp_windows_required")
	}
	r, raw, err := s.loadRegistration(ctx)
	if err != nil {
		return err
	}
	binding := localAudioCppBinding(raw)
	if err := s.verifyRegistrationFiles(r); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("audio_cpp_service_closed")
	}
	s.mu.Unlock()
	logDir := filepath.Join(s.dataDir, "station-audio-cpp-logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return errors.New("audio_cpp_log_directory_unavailable")
	}
	logPath := filepath.Join(logDir, localAudioCppFishHash(fish)+".log")
	check := func(checkCtx context.Context) error {
		current, currentRaw, loadErr := s.loadRegistration(checkCtx)
		if loadErr != nil || localAudioCppBinding(currentRaw) != binding || current.Engine.Server != r.Engine.Server {
			return errors.New("audio_cpp_registration_changed")
		}
		return s.verifyRegistrationFiles(current)
	}
	args := []string{"--config", r.ServerConfig, "--ui", "--host", r.Host, "--port", strconv.Itoa(r.Port), "--backend", r.Backend, "--log", "--log-file", logPath}
	env := localAudioCppEnvironment()
	serviceID := localAudioCppServiceIDForFish(fish)
	_, err = s.services.Start(ctx, serviceID, pluginruntime.ManagedServiceSpec{
		OwnerPlugin: localAudioCppOwner, Binding: binding, Executable: r.Engine.Server,
		Arguments: args, Directory: filepath.Dir(r.Engine.Server), Environment: env,
		LogPath: logPath, PendingJobsPath: filepath.Join(s.dataDir, "station-audio-cpp-jobs.json"),
		StopTimeout: 10 * time.Second, Check: check,
	}, func(admitCtx context.Context, launch func() error) error {
		listener, listenErr := net.Listen("tcp", net.JoinHostPort(r.Host, strconv.Itoa(r.Port)))
		if listenErr != nil {
			return errors.New("audio_cpp_port_occupied_no_adoption")
		}
		_ = listener.Close()
		return launch()
	})
	if err != nil {
		return err
	}
	if err := s.waitHealthy(ctx, r, binding); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = s.services.Stop(stopCtx, serviceID)
		cancel()
		return err
	}
	return nil
}

func (s *localAudioCppService) Stop(ctx context.Context) error {
	return s.services.StopOwner(ctx, localAudioCppOwner)
}

func (s *localAudioCppService) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.services.Close(ctx)
}

func (s *localAudioCppService) Synthesize(ctx context.Context, fish, binding string, request localTTSRequest) (localTTSResult, error) {
	if err := localTTSValidateRequest(request); err != nil {
		return localTTSResult{}, err
	}
	r, raw, err := s.loadRegistration(ctx)
	if err != nil {
		return localTTSResult{}, err
	}
	if binding == "" || binding != localAudioCppBinding(raw) || request.VoiceID != "kokoro" {
		return localTTSResult{}, errors.New("audio_cpp_deployment_changed")
	}
	if request.Speed != nil && (*request.Speed < 0.5 || *request.Speed > 2.0) {
		return localTTSResult{}, errors.New("audio_cpp_speed_not_supported")
	}
	if err := s.Start(ctx, fish); err != nil {
		return localTTSResult{}, err
	}
	var body struct {
		Model    string `json:"model"`
		Input    string `json:"input"`
		Language string `json:"language,omitempty"`
	}
	body.Model, body.Input, body.Language = r.TTSModelID, request.Text, request.Language
	if body.Language == "" {
		body.Language = "zh"
	}
	data, _ := json.Marshal(body)
	wav, err := s.do(ctx, r, binding, http.MethodPost, "/v1/audio/speech", "application/json", bytes.NewReader(data), "audio/wav")
	if err != nil {
		return localTTSResult{}, err
	}
	if _, latestRaw, latestErr := s.loadRegistration(ctx); latestErr != nil || localAudioCppBinding(latestRaw) != binding {
		return localTTSResult{}, errors.New("audio_cpp_deployment_changed")
	}
	rate, seconds, err := mediaspec.PCM16WAVInfo(wav)
	if err != nil || len(wav) > localAudioCppMaxWAV || rate < 8000 || rate > 192000 || seconds <= 0 {
		return localTTSResult{}, errors.New("audio_cpp_output_invalid")
	}
	speed := 1.0
	if request.Speed != nil {
		speed = *request.Speed
	}
	return localTTSResult{VoiceID: "kokoro", Version: binding, Speed: speed, WAV: wav}, nil
}

func (s *localAudioCppService) Transcribe(ctx context.Context, fish, binding string, wav []byte) (string, error) {
	if len(wav) == 0 || len(wav) > localAudioCppMaxWAV {
		return "", errors.New("audio_cpp_audio_invalid")
	}
	if _, _, err := mediaspec.PCM16WAVInfo(wav); err != nil {
		return "", errors.New("audio_cpp_audio_invalid")
	}
	r, raw, err := s.loadRegistration(ctx)
	if err != nil {
		return "", err
	}
	if binding == "" || binding != localAudioCppBinding(raw) {
		return "", errors.New("audio_cpp_deployment_changed")
	}
	if err := s.Start(ctx, fish); err != nil {
		return "", err
	}
	var payload bytes.Buffer
	mw := multipart.NewWriter(&payload)
	_ = mw.WriteField("model", r.ASRModelID)
	_ = mw.WriteField("language", "zh")
	part, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err = part.Write(wav); err != nil {
		return "", err
	}
	if err = mw.Close(); err != nil {
		return "", err
	}
	data, err := s.do(ctx, r, binding, http.MethodPost, "/v1/audio/transcriptions", mw.FormDataContentType(), &payload, "application/json")
	if err != nil {
		return "", err
	}
	if _, latestRaw, latestErr := s.loadRegistration(ctx); latestErr != nil || localAudioCppBinding(latestRaw) != binding {
		return "", errors.New("audio_cpp_deployment_changed")
	}
	var response struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &response) != nil || strings.TrimSpace(response.Text) == "" || !utf8.ValidString(response.Text) {
		return "", errors.New("audio_cpp_transcription_invalid")
	}
	return strings.TrimSpace(response.Text), nil
}

func (s *localAudioCppService) do(ctx context.Context, r localAudioCppRegistration, binding, method, route, contentType string, body io.Reader, accept string) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, "http://"+net.JoinHostPort(r.Host, strconv.Itoa(r.Port))+route, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", accept)
	response, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, errors.New("audio_cpp_request_failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, localAudioCppMaxWAV+1))
	if err != nil || len(data) > localAudioCppMaxWAV || response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audio_cpp_request_status_%d", response.StatusCode)
	}
	return data, nil
}

func (s *localAudioCppService) waitHealthy(ctx context.Context, r localAudioCppRegistration, binding string) error {
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	for {
		if err := s.healthOnce(ctx, r, binding); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("audio_cpp_start_timeout")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *localAudioCppService) healthOnce(ctx context.Context, r localAudioCppRegistration, binding string) error {
	data, err := s.do(ctx, r, binding, http.MethodGet, "/health", "", http.NoBody, "application/json")
	if err != nil || !bytes.Contains(data, []byte(`"status"`)) {
		return errors.New("audio_cpp_health_unavailable")
	}
	data, err = s.do(ctx, r, binding, http.MethodGet, "/v1/models", "", http.NoBody, "application/json")
	if err != nil {
		return err
	}
	var models struct {
		Data []struct {
			ID         string `json:"id"`
			Configured bool   `json:"configured"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &models) != nil {
		return errors.New("audio_cpp_models_invalid")
	}
	seen := map[string]bool{}
	for _, model := range models.Data {
		if model.Configured {
			seen[model.ID] = true
		}
	}
	if !seen[r.TTSModelID] || !seen[r.ASRModelID] {
		return errors.New("audio_cpp_models_not_ready")
	}
	return nil
}

func (s *localAudioCppService) loadRegistration(ctx context.Context) (localAudioCppRegistration, []byte, error) {
	if err := ctx.Err(); err != nil {
		return localAudioCppRegistration{}, nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.dataDir, localAudioCppRegistrationFile))
	if err != nil || len(data) == 0 || len(data) > localAudioCppMaxConfig {
		return localAudioCppRegistration{}, nil, errors.New("audio_cpp_not_installed_or_authorized")
	}
	var r localAudioCppRegistration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return localAudioCppRegistration{}, nil, errors.New("audio_cpp_registration_invalid")
	}
	if !r.Enabled || r.SchemaVersion != 1 || r.Runtime != "audio.cpp" || r.Host != "127.0.0.1" || r.Port < 1024 || r.Port > 65535 || (r.Backend != "cpu" && r.Backend != "cuda") || r.TTSModelID == "" || r.ASRModelID == "" {
		return localAudioCppRegistration{}, nil, errors.New("audio_cpp_registration_invalid")
	}
	if r.Permissions.Network != "loopback_only" || !r.Permissions.ModelRead || !r.Permissions.ArtifactWrite || r.Permissions.ExternalDelivery {
		return localAudioCppRegistration{}, nil, errors.New("audio_cpp_permission_invalid")
	}
	if !filepath.IsAbs(r.Engine.Server) || !filepath.IsAbs(r.ServerConfig) || !localAudioCppRegular(r.Engine.Server) || !localAudioCppRegular(r.ServerConfig) || len(r.Models) == 0 {
		return localAudioCppRegistration{}, nil, errors.New("audio_cpp_registration_invalid")
	}
	seen := map[string]bool{}
	for _, m := range r.Models {
		if m.ID == "" || seen[m.ID] || (m.Task != "tts" && m.Task != "asr") || !filepath.IsAbs(m.Path) || !localAudioCppRegular(m.Path) || !localAudioCppHex.MatchString(m.SHA256) {
			return localAudioCppRegistration{}, nil, errors.New("audio_cpp_model_registration_invalid")
		}
		seen[m.ID] = true
	}
	if !seen[r.TTSModelID] || !seen[r.ASRModelID] {
		return localAudioCppRegistration{}, nil, errors.New("audio_cpp_model_selection_invalid")
	}
	if err := localAudioCppValidateServerConfig(r); err != nil {
		return localAudioCppRegistration{}, nil, err
	}
	return r, data, nil
}

func localAudioCppValidateServerConfig(r localAudioCppRegistration) error {
	data, err := os.ReadFile(r.ServerConfig)
	if err != nil || len(data) == 0 || len(data) > localAudioCppMaxConfig {
		return errors.New("audio_cpp_server_config_invalid")
	}
	var config localAudioCppServerConfig
	if json.Unmarshal(data, &config) != nil || config.Host != r.Host || config.Port != r.Port || config.Backend != r.Backend || filepath.Clean(config.ModelsRoot) != filepath.Clean(r.ModelsRoot) {
		return errors.New("audio_cpp_server_config_invalid")
	}
	registered := make(map[string]string, len(r.Models))
	for _, model := range r.Models {
		registered[model.ID] = filepath.Clean(model.Path)
	}
	selected := map[string]bool{r.TTSModelID: true, r.ASRModelID: true}
	seen := map[string]bool{}
	for _, model := range config.Models {
		if !selected[model.ID] {
			continue
		}
		if seen[model.ID] || filepath.Clean(model.Path) != registered[model.ID] {
			return errors.New("audio_cpp_server_config_invalid")
		}
		seen[model.ID] = true
	}
	if !seen[r.TTSModelID] || !seen[r.ASRModelID] {
		return errors.New("audio_cpp_server_config_invalid")
	}
	return nil
}

func (s *localAudioCppService) verifyRegistrationFiles(r localAudioCppRegistration) error {
	if !localAudioCppHashMatches(r.Engine.Server, "") || !localAudioCppRegular(r.Engine.Server) {
		return errors.New("audio_cpp_server_unavailable")
	}
	for _, m := range r.Models {
		if !localAudioCppHashMatches(m.Path, m.SHA256) {
			return errors.New("audio_cpp_model_changed")
		}
	}
	return nil
}

func localAudioCppHashMatches(path, expected string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 8<<30)); err != nil {
		return false
	}
	if expected == "" {
		return true
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expected)
}

func localAudioCppRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func localAudioCppBinding(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "audio-cpp-" + hex.EncodeToString(sum[:])
}

func localAudioCppFishHash(fish string) string {
	sum := sha256.Sum256([]byte(fish))
	return hex.EncodeToString(sum[:8])
}

func localAudioCppServiceIDForFish(fish string) string {
	return localAudioCppServiceID + ":" + localAudioCppFishHash(fish)
}

func validLocalAudioCppFish(fish string) bool {
	return fish != "" && len(fish) <= 160 && !strings.ContainsAny(fish, `\\/:*?"<>|`)
}

func localAudioCppEnvironment() []string {
	keep := []string{"SystemRoot", "WINDIR", "SystemDrive", "PATH", "TEMP", "TMP", "CUDA_PATH"}
	result := make([]string, 0, len(keep))
	for _, key := range keep {
		if value := os.Getenv(key); value != "" {
			result = append(result, key+"="+value)
		}
	}
	return append(result, "AUDIO_CPP_OFFLINE=1")
}
