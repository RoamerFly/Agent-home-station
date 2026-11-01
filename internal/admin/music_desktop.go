package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/agenthost"
)

const windowsMusicPolicy = "registry-read-private-network-v1"

type windowsMusicConfig struct {
	Enabled bool   `json:"enabled"`
	Policy  string `json:"policy"`
	Python  string `json:"python"`
	Entry   string `json:"entry"`
	Media   string `json:"media"`
	IPC     string `json:"ipc"`
}

type windowsMusicProcess struct {
	command *exec.Cmd
	input   io.WriteCloser
	done    chan struct{}
}

func regularMusicPath(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("windows_music_path_not_canonical")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("windows_music_resource_unavailable_or_linked")
		}
		if current == path && (directory && !info.IsDir() || !directory && !info.Mode().IsRegular()) {
			return errors.New("windows_music_resource_type_invalid")
		}
		if next := filepath.Dir(current); next == current {
			break
		}
	}
	return nil
}

func (m *Manager) startWindowsMusic(ctx context.Context) (string, error) {
	if m.draining() {
		return "", toolInputError("windows_music_start_paused_for_update")
	}
	m.musicProcessMu.Lock()
	defer m.musicProcessMu.Unlock()
	if m.windowsMusic != nil {
		select {
		case <-m.windowsMusic.done:
			m.windowsMusic = nil
		default:
			return `{"state":"starting_or_active","backend":"windows-native"}`, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(m.dir, "windows-music.json"))
	var config windowsMusicConfig
	if err != nil || len(data) > 16384 || json.Unmarshal(data, &config) != nil || !config.Enabled || config.Policy != windowsMusicPolicy {
		return "", toolInputError("windows_music_not_installed_or_authorized")
	}
	public := filepath.Join(m.managedRoot(), "public_workspace")
	pythonRoot := filepath.Join(public, "runtimes") + string(os.PathSeparator)
	if !strings.HasPrefix(strings.ToLower(config.Python), strings.ToLower(pythonRoot)) || !strings.EqualFold(filepath.Base(config.Python), "python.exe") || filepath.Base(config.Entry) != "managed_music_service.py" {
		return "", toolInputError("windows_music_runtime_identity_invalid")
	}
	for _, resource := range []struct {
		path string
		dir  bool
	}{{config.Python, false}, {config.Entry, false}, {config.Media, true}, {config.IPC, true}} {
		if err := regularMusicPath(resource.path, resource.dir); err != nil {
			return "", err
		}
	}
	// Control code, authentication and IPC leases stay outside inference mounts.
	for _, path := range []string{config.Entry, config.IPC} {
		if pathWithin(m.managedRoot(), path) {
			return "", toolInputError("windows_music_control_inside_agent_workspace")
		}
	}
	localEndpoint, localToken, err := m.localMusicPluginConnection(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(localEndpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", toolInputError("windows_music_requires_loopback_endpoint")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1024 || port > 65535 {
		return "", toolInputError("windows_music_port_invalid")
	}
	listener, err := net.Listen("tcp", u.Host)
	if err != nil {
		return "", toolInputError("windows_music_port_occupied_no_adoption")
	}
	listener.Close()
	private := m.privateWorkspaceFor(m.native.Account())
	if err := ensureManaged(private); err != nil {
		return "", err
	}
	scratch := filepath.Join(private, "scratch")
	if err := os.MkdirAll(scratch, 0755); err != nil {
		return "", err
	}
	token := filepath.Join(m.dir, "windows-music-token")
	if localToken == "" {
		return "", toolInputError("windows_music_authentication_not_configured")
	}
	if err := writePrivate(token, []byte(localToken)); err != nil {
		return "", err
	}
	command := exec.Command(config.Python, "-X", "utf8", config.Entry, "--workspace", m.managedRoot(), "--private", private,
		"--python", config.Python, "--token-file", token, "--ipc", config.IPC, "--bind", "127.0.0.1", "--port", strconv.Itoa(port), "--desktop")
	command.Dir = filepath.Dir(config.Entry)
	command.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "WINDIR=" + os.Getenv("WINDIR"), "SystemDrive=" + os.Getenv("SystemDrive"),
		"PATH=" + config.Media + string(os.PathListSeparator) + filepath.Join(os.Getenv("SystemRoot"), "System32"),
		"WECHAT_WINDOWS_MUSIC_POLICY=" + config.Policy, "WECHAT_WINDOWS_MUSIC_MEDIA=" + config.Media,
		"TEMP=" + scratch, "TMP=" + scratch, "APPDATA=" + scratch, "LOCALAPPDATA=" + scratch, "USERPROFILE=" + scratch,
		"USERNAME=managed-music",
		"HF_HOME=" + filepath.Join(private, "scratch", "hf-cache"), "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "PYTHONDONTWRITEBYTECODE=1"}
	configureMusicProcess(command)
	input, err := command.StdinPipe()
	if err != nil {
		return "", err
	}
	log, err := os.OpenFile(filepath.Join(m.dir, "windows-music.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		input.Close()
		return "", err
	}
	command.Stdout, command.Stderr = log, log
	// Serialize final admission with the update barrier, after disk checks.
	m.mu.Lock()
	_, holdError := os.Lstat(m.memoryUpdateHoldPath())
	if m.drainTicket != "" || !errors.Is(holdError, os.ErrNotExist) {
		m.mu.Unlock()
		input.Close()
		log.Close()
		return "", toolInputError("windows_music_start_paused_for_update")
	}
	err = command.Start()
	m.mu.Unlock()
	if err != nil {
		input.Close()
		log.Close()
		return "", toolInputError("windows_music_start_failed_check_private_log")
	}
	process := &windowsMusicProcess{command: command, input: input, done: make(chan struct{})}
	m.windowsMusic = process
	go func() {
		_ = command.Wait()
		input.Close()
		log.Close()
		close(process.done)
	}()
	return `{"state":"starting_or_active","backend":"windows-native","instruction":"用music_service_status核对模型就绪；启动不代表任务完成。"}`, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func (m *Manager) runWindowsNamedModel(ctx context.Context, cfg agenthost.Config, cwd string, argv []string) ([]byte, []byte, error) {
	if cfg.Isolation == nil {
		return nil, nil, errors.New("sandbox_required")
	}
	data, err := os.ReadFile(filepath.Join(m.dir, "windows-music.json"))
	var config windowsMusicConfig
	if err != nil || len(data) > 16384 || json.Unmarshal(data, &config) != nil || !config.Enabled || config.Policy != windowsMusicPolicy || len(argv) == 0 || argv[0] != config.Python {
		return nil, nil, errors.New("windows_named_model_runtime_not_authorized")
	}
	if err := regularMusicPath(config.Media, true); err != nil {
		return nil, nil, err
	}
	cfg.Isolation.ReadOnly = append(append([]string{}, cfg.Isolation.ReadOnly...), config.Media)
	nltkData := filepath.Join(m.managedRoot(), "public_workspace", "runtimes", "windows-gpt-nltk-data")
	if err := regularMusicPath(nltkData, true); err != nil {
		return nil, nil, errors.New("windows_gpt_nltk_data_missing: install verified pronunciation data before inference")
	}
	for _, name := range []string{"corpora/cmudict.zip", "taggers/averaged_perceptron_tagger.zip", "taggers/averaged_perceptron_tagger_eng.zip", "tokenizers/punkt_tab.zip"} {
		if err := regularMusicPath(filepath.Join(nltkData, filepath.FromSlash(name)), false); err != nil {
			return nil, nil, errors.New("windows_gpt_nltk_data_incomplete: " + name)
		}
	}
	scratch := cwd
	if len(cfg.Roots) > 0 {
		scratch = cfg.Roots[0]
	}
	environment := map[string]string{"PATH": config.Media + string(os.PathListSeparator) + filepath.Join(os.Getenv("SystemRoot"), "System32"),
		"USERNAME": "managed-music", "WECHAT_WINDOWS_MUSIC_RUNTIME": "1", "NLTK_DATA": nltkData, "HF_HOME": filepath.Join(scratch, "hf-cache"),
		"HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "PYTHONDONTWRITEBYTECODE": "1", "PYTHONUTF8": "1",
		"MPLCONFIGDIR": filepath.Join(scratch, "matplotlib"), "NUMBA_CACHE_DIR": filepath.Join(scratch, "numba-cache")}
	return agenthost.RunMusicIsolated(ctx, cfg, cwd, argv, config.Policy, environment)
}

func (m *Manager) closeWindowsMusic() {
	m.musicProcessMu.Lock()
	process := m.windowsMusic
	m.windowsMusic = nil
	m.musicProcessMu.Unlock()
	if process == nil {
		return
	}
	_, _ = io.WriteString(process.input, "stop\n")
	process.input.Close()
	select {
	case <-process.done:
	case <-time.After(50 * time.Second):
		// Only the owned supervisor is stopped. Its LPAC child Job Objects
		// close with its process; the desktop also owns the complete tree.
		_ = process.command.Process.Kill()
		<-process.done
	}
}

func (m *Manager) windowsMusicBusy() bool {
	m.musicProcessMu.Lock()
	process := m.windowsMusic
	m.musicProcessMu.Unlock()
	if process == nil {
		return false
	}
	select {
	case <-process.done:
		return false
	default:
	}
	data, err := os.ReadFile(filepath.Join(m.privateWorkspaceFor(m.native.Account()), "music", "jobs", "jobs.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	var jobs map[string]struct {
		State string `json:"state"`
	}
	if err != nil || len(data) > 32<<20 || json.Unmarshal(data, &jobs) != nil {
		return true
	}
	for _, job := range jobs {
		if job.State == "queued" || job.State == "running" {
			return true
		}
	}
	return false
}
