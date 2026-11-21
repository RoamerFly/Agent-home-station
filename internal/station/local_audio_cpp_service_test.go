package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/mediaspec"
)

func TestAudioCppTranscriptRequiresRecognizedWords(t *testing.T) {
	for _, input := range []string{`{"text":" 。…？！ "}`, `{"text":""}`} {
		if text, err := decodeAudioCppTranscript([]byte(input)); text != "" || !mediaspec.IsNoSpeech(err) {
			t.Fatalf("empty speech outcome: %q, %v", text, err)
		}
	}
	if _, err := decodeAudioCppTranscript([]byte(`{}`)); err == nil || mediaspec.IsNoSpeech(err) {
		t.Fatalf("missing field: %v", err)
	}
	if text, err := decodeAudioCppTranscript([]byte(`{"text":" 走。 "}`)); text != "走。" || err != nil {
		t.Fatalf("valid short utterance: %q, %v", text, err)
	}
}

func TestLocalAudioCppPhonemizerPinsLibraryAndData(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "libespeak-ng.dll")
	data := filepath.Join(root, "espeak-ng-data")
	server := filepath.Join(root, "audiocpp_server.exe")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{library, server, filepath.Join(data, "phontab"), filepath.Join(data, "phonindex"), filepath.Join(data, "phondata")} {
		if err := os.WriteFile(path, []byte("synthetic-"+filepath.Base(path)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256([]byte("synthetic-libespeak-ng.dll"))
	registration := localAudioCppRegistration{Engine: localAudioCppEngineRegistration{Server: server},
		Phonemizer: &localAudioCppPhonemizerRegistration{Library: library, LibrarySHA256: hex.EncodeToString(digest[:]), Data: data}}
	service := &localAudioCppService{}
	if err := service.verifyRegistrationFiles(registration); err != nil {
		t.Fatal(err)
	}
	env := strings.Join(localAudioCppEnvironment(registration), "\n")
	if !strings.Contains(env, "ESPEAK_DATA_PATH="+data) || !strings.Contains(env, "PATH="+root+string(os.PathListSeparator)) {
		t.Fatal("pinned phonemizer dependency was not delivered to the managed child")
	}
	if err := os.WriteFile(library, []byte("changed DLL"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.verifyRegistrationFiles(registration); err == nil {
		t.Fatal("modified phonemizer DLL was accepted")
	}
}

func TestLiveAudioCppRegisteredModelsSynthesizeAndTranscribe(t *testing.T) {
	registrationPath := os.Getenv("AHS_AUDIO_CPP_REGISTRATION")
	if registrationPath == "" {
		t.Skip("set AHS_AUDIO_CPP_REGISTRATION to the installed Windows registration")
	}
	raw, err := os.ReadFile(registrationPath)
	if err != nil {
		t.Fatal(err)
	}
	var registration localAudioCppRegistration
	if err := json.Unmarshal(raw, &registration); err != nil || registration.Phonemizer == nil {
		t.Fatalf("installed audio.cpp phonemizer registration unavailable: %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dataDir, editableRoot := t.TempDir(), t.TempDir()
	registration.Backend, registration.Port = "cpu", port
	registration.ServerConfig = filepath.Join(dataDir, "server.json")
	selected := make([]localAudioCppModelRegistration, 0, 2)
	for _, model := range registration.Models {
		if model.ID == registration.TTSModelID || model.ID == registration.ASRModelID {
			selected = append(selected, model)
		}
	}
	registration.Models = selected
	serverConfig := localAudioCppServerConfig{Host: registration.Host, Port: port, Backend: "cpu", ModelsRoot: registration.ModelsRoot, LazyLoad: true, MaxLoadedModels: 2}
	for _, model := range selected {
		entry := localAudioCppServerModel{ID: model.ID, Path: model.Path, Task: model.Task, Family: model.Family, Mode: "offline", Options: map[string]any{"language": "zh"}}
		if model.Task == "asr" {
			entry.Options["audio_chunk_mode"] = "none"
		} else {
			entry.Voice = map[string]string{"voice_id": "zf_xiaobei"}
			entry.Options["seed"] = 42
		}
		serverConfig.Models = append(serverConfig.Models, entry)
	}
	writeJSON := func(path string, value any) {
		t.Helper()
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(path, data, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	writeJSON(registration.ServerConfig, serverConfig)
	writeJSON(filepath.Join(dataDir, localAudioCppRegistrationFile), registration)
	service := newLocalAudioCppService(dataDir, editableRoot, "")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Error(err)
		}
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	const fish = "synthetic-audio-cpp-owner"
	info, err := service.Info(ctx, fish)
	if err != nil || info.TTSModelID != "kokoro" || info.ASRModelID != "sensevoice" || len(info.Models) != 2 {
		t.Fatalf("registered models: %+v %v", info, err)
	}
	result, err := service.Synthesize(ctx, fish, info.Binding, localTTSRequest{VoiceID: "kokoro", Text: "你好，这是音频工具测试。", Language: "zh"})
	if err != nil {
		t.Fatalf("real Kokoro synthesis: %v", err)
	}
	rate, seconds, err := mediaspec.PCM16WAVInfo(result.WAV)
	if err != nil || len(result.WAV) < 1000 || rate != 24000 || seconds <= 0 {
		t.Fatalf("real Kokoro WAV: bytes=%d rate=%d duration=%f err=%v", len(result.WAV), rate, seconds, err)
	}
	transcript, err := service.Transcribe(ctx, fish, info.Binding, result.WAV)
	if err != nil || transcript == "" {
		t.Fatalf("real SenseVoice transcription: %v", err)
	}
	t.Logf("registered audio.cpp models and actual CPU backend produced a %d-byte WAV at %d Hz for %.2fs, then recognized %d transcript characters; no WeChat send", len(result.WAV), rate, seconds, len([]rune(transcript)))
}

func TestLocalAudioCppRegistrationInfoAndPermissionFence(t *testing.T) {
	data, root := t.TempDir(), t.TempDir()
	server := filepath.Join(data, "audiocpp_server.exe")
	config := filepath.Join(data, "server.json")
	modelTTS := filepath.Join(data, "kokoro.gguf")
	modelASR := filepath.Join(data, "sensevoice.gguf")
	for _, path := range []string{server, config, modelTTS, modelASR} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	serverConfig, _ := json.Marshal(localAudioCppServerConfig{Host: "127.0.0.1", Port: 8192, Backend: "cpu", ModelsRoot: data, Models: []localAudioCppServerModel{{ID: "kokoro", Path: modelTTS, Task: "tts"}, {ID: "sensevoice", Path: modelASR, Task: "asr"}}})
	if err := os.WriteFile(config, serverConfig, 0600); err != nil {
		t.Fatal(err)
	}
	hashFile := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	registration := localAudioCppRegistration{
		SchemaVersion: 1, Enabled: true, Runtime: "audio.cpp", Backend: "cpu", Host: "127.0.0.1", Port: 8192,
		ModelsRoot: data, ServerConfig: config, Engine: localAudioCppEngineRegistration{Version: "test", Commit: "test", Server: server},
		Models:      []localAudioCppModelRegistration{{ID: "kokoro", Family: "kokoro_tts", Task: "tts", Path: modelTTS, SHA256: hashFile(modelTTS)}, {ID: "sensevoice", Family: "sense_asr", Task: "asr", Path: modelASR, SHA256: hashFile(modelASR)}},
		Permissions: localAudioCppPermissions{ModelRead: true, ArtifactWrite: true, Network: "loopback_only"}, TTSModelID: "kokoro", ASRModelID: "sensevoice",
	}
	raw, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(data, localAudioCppRegistrationFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	service := newLocalAudioCppService(data, root, "")
	info, err := service.Info(context.Background(), "fish-test")
	if err != nil {
		t.Fatal(err)
	}
	if info.Backend != "cpu" || info.Version != "test" || info.Binding == "" || len(info.Models) != 2 {
		t.Fatalf("unexpected deployment: %#v", info)
	}
	registration.Permissions.Network = "internet"
	bad, _ := json.Marshal(registration)
	if err = os.WriteFile(filepath.Join(data, localAudioCppRegistrationFile), bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Info(context.Background(), "fish-test"); err == nil {
		t.Fatal("network permission was accepted")
	}
	_ = service.Close(context.Background())
}
