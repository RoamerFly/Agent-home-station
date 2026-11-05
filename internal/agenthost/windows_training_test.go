package agenthost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestWindowsMusicRegistrationCannotBeSelectedByConfigurationJSON(t *testing.T) {
	var config WindowsConfig
	if err := json.Unmarshal([]byte(`{"pythonw":"registered","music_registration":"agent-selected-private-profile","MusicRegistration":"also-forbidden"}`), &config); err != nil {
		t.Fatal(err)
	}
	if config.MusicRegistration != "" {
		t.Fatal("JSON acquired private music registration authority")
	}
	config.MusicRegistration = "host-only-registration"
	raw, err := json.Marshal(config)
	if err != nil || bytes.Contains(raw, []byte(config.MusicRegistration)) {
		t.Fatal("private registration exposed as configuration", err)
	}
}

func TestLiveWindowsTrainingRegisteredCodeThroughGoHost(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("WECHAT_TRAINING_HOST_TEST") != "1" {
		t.Skip("opt-in project-local synthetic LPAC training-code test")
	}
	local, err := filepath.Abs("../../dist/windows-native/runtimes/windows-python-cli")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	public, private := filepath.Join(root, "workspace", "public_workspace"), filepath.Join(root, "workspace", "pri_workspace", "synthetic-account")
	pythonRoot := filepath.Join(public, "runtimes", "test-python")
	agent, music, pkg := filepath.Join(root, "deployment", "runtimes", "windows-agent-host"), filepath.Join(root, "deployment", "runtimes", "windows-music"), filepath.Join(root, "deployment", "plugins", "music")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(pythonRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.CopyFS(pythonRoot, os.DirFS(local)); err != nil {
		t.Fatal("copy project-local plain Python fixture", err)
	}
	for _, name := range []string{"windows_agent_host.pyw", "windows_lpac.py"} {
		body, err := os.ReadFile(filepath.Join("../../scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(agent, name), body)
	}
	controlFiles := map[string]string{}
	for _, name := range []string{"managed_music_runner.py", "rvc_train_trial.py", "managed_music_service.py"} {
		body, err := os.ReadFile(filepath.Join("../../scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(music, name), body)
		sum := sha256.Sum256(body)
		controlFiles[name] = hex.EncodeToString(sum[:])
	}
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "platform": "windows-x64", "policy": "registry-read-private-network-v1", "files": controlFiles})
	write(filepath.Join(music, "runtime-manifest.json"), raw)
	const entry = "runtime/training/rvc_train_trial.py"
	const requirements = "runtime/training/requirements.txt"
	files := map[string]string{entry: "import json,sys\nprint(json.dumps(sys.argv[1:]))\n", requirements: "# synthetic no installation\n", "runtime/main.py": "# synthetic unused entry\n", "capabilities.json": "{}", "config.schema.json": "{}", "ui.schema.json": "{}", "README.md": "synthetic", "PERMISSIONS.md": "synthetic", "MIGRATION.md": "synthetic", "CHANGELOG.md": "synthetic"}
	index := map[string]string{}
	for name, body := range files {
		write(filepath.Join(pkg, filepath.FromSlash(name)), []byte(body))
		sum := sha256.Sum256([]byte(body))
		index[name] = hex.EncodeToString(sum[:])
	}
	manifest, _ := json.Marshal(map[string]any{"id": "music", "schema_version": 1, "host_protocol": 1, "version": "1.0.0", "runtime": map[string]string{"transport": "mcp-stdio", "entrypoint": "runtime/main.py"}, "files": index})
	write(filepath.Join(pkg, "plugin.json"), manifest)
	digest := sha256.Sum256(manifest)
	rvc, _ := json.Marshal(map[string]any{"training_owner": "plugin:music", "training_package_directory": pkg, "training_package_sha256": hex.EncodeToString(digest[:]), "training_entry": filepath.Join(pkg, filepath.FromSlash(entry)), "training_requirements": filepath.Join(pkg, filepath.FromSlash(requirements))})
	write(filepath.Join(public, "manifests", "rvc.json"), rvc)
	registration := filepath.Join(root, "admin", "windows-music.json")
	profile, _ := json.Marshal(map[string]any{"enabled": true, "policy": "registry-read-private-network-v1", "python": filepath.Join(pythonRoot, "python.exe"), "entry": filepath.Join(music, "managed_music_service.py")})
	write(registration, profile)
	cfg := Defaults()
	cfg.Mode = "full"
	cfg.Roots = []string{private}
	cfg.Isolation = &Isolation{GPU: true, ReadOnly: []string{public}}
	cfg.Windows = &WindowsConfig{Pythonw: filepath.Join(local, "pythonw.exe"), Helper: filepath.Join(agent, "windows_agent_host.pyw"), Spool: filepath.Join(root, "spool"), MusicRegistration: registration}
	arguments := []string{"--project", "原 checkpoint", "--resume-from", "original-task", "--device", "cuda:0", "", "literal %COMSPEC% & $()", "trailing path\\", "train"}
	argv := append([]string{filepath.Join(pythonRoot, "python.exe"), "-X", "utf8", filepath.Join(pkg, filepath.FromSlash(entry))}, arguments...)
	ctx, cancel := context.WithTimeout(t.Context(), 3625*time.Millisecond)
	defer cancel()
	output, err := RunIsolated(ctx, cfg, private, argv)
	if err != nil {
		t.Fatal("Go private transport did not admit verified training code", err, string(output))
	}
	var got []string
	if json.Unmarshal(output, &got) != nil || !reflect.DeepEqual(got, arguments) {
		t.Fatal("actual Go/host/LPAC changed training argv", string(output))
	}
	requests, err := os.ReadDir(cfg.Windows.Spool)
	if err != nil || len(requests) != 1 {
		t.Fatal("missing private host request", err)
	}
	raw, err = os.ReadFile(filepath.Join(cfg.Windows.Spool, requests[0].Name(), "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var effective struct {
		GPU     bool    `json:"gpu_authorized"`
		Profile string  `json:"music_registration"`
		Timeout float64 `json:"timeout_seconds"`
		Policy  string  `json:"music_policy"`
	}
	if json.Unmarshal(raw, &effective) != nil || !effective.GPU || effective.Profile != registration || effective.Timeout <= 3 || effective.Timeout > 3.625 || effective.Policy != "" {
		t.Fatal("private profile/deadline/capabilities changed", effective)
	}
}
