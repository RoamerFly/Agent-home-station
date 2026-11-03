package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/pluginruntime"
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

const windowsMusicServiceID = "music.local"

// Manager owns one generic registry; the profile resolver below never retains
// commands, pipes, PIDs or service lifecycle state.
func (m *Manager) managedServiceRegistry() *pluginruntime.ManagedServices {
	m.managedServicesOnce.Do(func() {
		if m.managedServices == nil {
			m.managedServices = &pluginruntime.ManagedServices{}
		}
	})
	return m.managedServices
}

func (m *Manager) windowsMusicStatus() pluginruntime.ManagedServiceStatus {
	return m.managedServiceRegistry().Status(windowsMusicServiceID)
}

// touchWindowsMusic renews the lease only for this account's registry-owned
// service.  It never probes a port or adopts another account's process.
func (m *Manager) touchWindowsMusic() bool {
	if m.managedServiceRegistry().Touch(windowsMusicServiceID) {
		m.refreshMusicIdleTimeout()
		return true
	}
	return false
}

// holdWindowsMusic prevents the idle reaper from reserving a running local
// supervisor while a bound local HTTP request is in flight. It is not a
// process handle and does not create or discover a service.
func (m *Manager) holdWindowsMusic() (func(), bool) {
	release, ok := m.managedServiceRegistry().Acquire(windowsMusicServiceID)
	if !ok {
		return nil, false
	}
	m.refreshMusicIdleTimeout()
	return func() {
		release()
		m.refreshMusicIdleTimeout()
	}, true
}

// refreshMusicIdleTimeout applies a changed administrator setting to an
// already-running local backend without restarting it.  A zero timeout is an
// explicit opt-out, not a request to stop a user-configured backend.
func (m *Manager) refreshMusicIdleTimeout() {
	m.scheduleWindowsMusicIdle(m.musicIdleTimeout(), 0)
}

func (m *Manager) scheduleWindowsMusicIdle(timeout, minimumDelay time.Duration) {
	// Query the registry before taking the timer lock. reclaim's guarded stop
	// takes these locks in the same order (registry then timer), avoiding a
	// configuration-save versus reaper lock inversion.
	status := m.windowsMusicStatus()
	idle, knownIdle := m.managedServiceRegistry().IdleFor(windowsMusicServiceID, time.Now())
	m.musicIdleMu.Lock()
	defer m.musicIdleMu.Unlock()
	m.musicIdleGeneration++
	generation := m.musicIdleGeneration
	if m.musicIdleTimer != nil {
		m.musicIdleTimer.Stop()
		m.musicIdleTimer = nil
	}
	if timeout <= 0 {
		return
	}
	if !status.Running || status.Stopping {
		return
	}
	delay := timeout
	if knownIdle {
		if idle >= timeout {
			delay = 0
		} else {
			delay = timeout - idle
		}
	}
	if minimumDelay > delay {
		delay = minimumDelay
	}
	m.musicIdleTimer = time.AfterFunc(delay, func() {
		m.reclaimWindowsMusicIdle(generation, timeout)
	})
}

func (m *Manager) reclaimWindowsMusicIdle(generation uint64, timeout time.Duration) {
	// StopIfIdle reserves the exact registry-owned process before releasing its
	// lock. The guard linearizes the reservation with settings changes: a stale
	// timer cannot stop a service after retention changed to zero or a longer
	// value.
	ctx, cancel := context.WithTimeout(context.Background(), 56*time.Second)
	stopped, stopErr := m.managedServiceRegistry().StopIfIdleGuarded(ctx, windowsMusicServiceID, timeout, func() bool {
		m.musicIdleMu.Lock()
		defer m.musicIdleMu.Unlock()
		if generation != m.musicIdleGeneration || timeout <= 0 || m.agent != nil && m.musicIdleTimeout() != timeout {
			return false
		}
		m.musicIdleTimer = nil
		return true
	})
	cancel()
	status := m.windowsMusicStatus()
	if !status.Running || status.Stopping || timeout <= 0 {
		return
	}
	if stopErr != nil {
		// Do not immediately re-arm an already-expired timer after a failed
		// shutdown. That can otherwise spin at zero delay on a broken backend.
		m.scheduleWindowsMusicIdle(timeout, time.Minute)
		return
	}
	if stopped {
		return
	}
	if status.Busy {
		// Jobs with an unknown provider/delivery receipt are deliberately busy.
		// Recheck later instead of retaining a completed backend forever.
		m.scheduleWindowsMusicIdle(timeout, time.Minute)
		return
	}
	m.refreshMusicIdleTimeout()
}

func (m *Manager) windowsMusicRegistered() bool {
	data, err := os.ReadFile(filepath.Join(m.dir, "windows-music.json"))
	var profile windowsMusicConfig
	return err == nil && len(data) <= 16384 && json.Unmarshal(data, &profile) == nil && profile.Enabled && profile.Policy == windowsMusicPolicy
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
	// The launch profile is a host-owned compatibility registration. Bind it
	// to the exact installed plugin/configuration; recheck before admission and
	// on repeated starts, including when a supervisor is already running.
	authority, err := m.musicServiceAuthority(ctx)
	if err != nil {
		return "", err
	}
	binding := namedSpeechSHA(append(append([]byte{}, data...), []byte(authority)...))
	check := func(ctx context.Context) error {
		if err := checkMusicServiceControl(ctx); err != nil {
			return err
		}
		current, err := os.ReadFile(filepath.Join(m.dir, "windows-music.json"))
		if err != nil || string(current) != string(data) {
			return toolInputError("windows_music_registration_changed")
		}
		currentAuthority, err := m.musicServiceAuthority(ctx)
		if err != nil || currentAuthority != authority {
			return toolInputError("windows_music_plugin_authorization_changed")
		}
		for _, resource := range []struct {
			path string
			dir  bool
		}{{config.Python, false}, {config.Entry, false}, {config.Media, true}, {config.IPC, true}} {
			if err := regularMusicPath(resource.path, resource.dir); err != nil {
				return err
			}
		}
		return nil
	}
	spec := pluginruntime.ManagedServiceSpec{
		OwnerPlugin: "music",
		Binding:     binding, Executable: config.Python,
		Arguments: []string{"-X", "utf8", config.Entry, "--workspace", m.managedRoot(), "--private", private,
			"--python", config.Python, "--token-file", token, "--ipc", config.IPC, "--bind", "127.0.0.1", "--port", strconv.Itoa(port), "--desktop"},
		Directory: filepath.Dir(config.Entry),
		Environment: []string{"SystemRoot=" + os.Getenv("SystemRoot"), "WINDIR=" + os.Getenv("WINDIR"), "SystemDrive=" + os.Getenv("SystemDrive"),
			"PATH=" + config.Media + string(os.PathListSeparator) + filepath.Join(os.Getenv("SystemRoot"), "System32"),
			"WECHAT_WINDOWS_MUSIC_POLICY=" + config.Policy, "WECHAT_WINDOWS_MUSIC_MEDIA=" + config.Media,
			"TEMP=" + scratch, "TMP=" + scratch, "APPDATA=" + scratch, "LOCALAPPDATA=" + scratch, "USERPROFILE=" + scratch,
			"USERNAME=managed-music", "HF_HOME=" + filepath.Join(scratch, "hf-cache"), "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "PYTHONDONTWRITEBYTECODE=1"},
		LogPath:         filepath.Join(m.dir, "windows-music.log"),
		PendingJobsPath: filepath.Join(private, "music", "jobs", "jobs.json"),
		StopInput:       "stop\n", StopTimeout: 50 * time.Second, Check: check,
	}
	_, err = m.managedServiceRegistry().Start(ctx, windowsMusicServiceID, spec, func(ctx context.Context, start func() error) error {
		if err := checkMusicServiceControl(ctx); err != nil {
			return err
		}
		// Refuse any listener we did not start. This runs only for a fresh
		// launch, so an owned live service is not mistaken for a foreign port.
		listener, err := net.Listen("tcp", u.Host)
		if err != nil {
			return toolInputError("windows_music_port_occupied_no_adoption")
		}
		listener.Close()
		if err = writePrivate(token, []byte(localToken)); err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		_, holdError := os.Lstat(m.memoryUpdateHoldPath())
		if m.drainTicket != "" || !errors.Is(holdError, os.ErrNotExist) {
			return toolInputError("windows_music_start_paused_for_update")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := start(); err != nil {
			return toolInputError("windows_music_start_failed_check_private_log")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	m.touchWindowsMusic()
	return `{"state":"starting_or_active","backend":"windows-native","instruction":"用music_service_status核对模型就绪；启动不代表任务完成。"}`, nil
}

// Only the currently installed, enabled plugin configuration confers launch
// authority. Retired speech settings never select the local service endpoint.
func (m *Manager) musicServiceAuthority(ctx context.Context) (string, error) {
	endpoint, token, err := m.localMusicPluginConnection(ctx)
	if err != nil {
		return "", err
	}
	store, err := m.packageStore()
	if err != nil {
		return "", err
	}
	installed, err := store.Installed(ctx, "music")
	if err != nil {
		return "", err
	}
	configuration, err := store.Configuration(ctx, "music")
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal([]any{store.Account(), installed, configuration.Revision, endpoint, token})
	return namedSpeechSHA(raw), nil
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
	m.musicIdleMu.Lock()
	m.musicIdleGeneration++
	if m.musicIdleTimer != nil {
		m.musicIdleTimer.Stop()
		m.musicIdleTimer = nil
	}
	m.musicIdleMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 56*time.Second)
	defer cancel()
	_ = m.managedServiceRegistry().Close(ctx)
}

func (m *Manager) windowsMusicBusy() bool {
	return m.windowsMusicStatus().Busy
}
