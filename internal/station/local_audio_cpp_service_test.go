package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
	service := newLocalAudioCppService(data, root)
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
