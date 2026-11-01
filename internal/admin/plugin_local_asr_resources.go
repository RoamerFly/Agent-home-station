package admin

// Local ASR resource registration for the media-understanding plugin. This is
// intentionally a host-side adapter: plugin configuration and capability
// arguments never choose an interpreter, SDK, model, token file or VAD file.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

const (
	localASRPluginID        = "media-understanding"
	localASRManifestLimit   = 64 << 10
	localASRRuntimeManifest = "runtime-manifest.json"
)

// registeredLocalASRManifest is the installed public-workspace contract. It
// deliberately includes only fields the plugin runner needs and provenance
// fields already emitted by scripts/install_local_asr.py.
type registeredLocalASRManifest struct {
	ID             string  `json:"id"`
	Root           string  `json:"root"`
	Python         string  `json:"python"`
	Entry          string  `json:"entry"`
	Packages       string  `json:"packages"`
	Model          string  `json:"model"`
	Tokens         string  `json:"tokens"`
	VADModel       string  `json:"vad_model"`
	VADSHA256      string  `json:"vad_sha256"`
	VADThreshold   float64 `json:"vad_threshold"`
	Threads        int     `json:"threads"`
	Language       string  `json:"language"`
	ChunkSeconds   float64 `json:"chunk_seconds"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

// localASRInitializer only names the host-registered profile. The bounded
// wechat/local_transcription service resolves interpreter/model paths itself;
// paths and command fragments are not exposed in plugin configuration or tool
// arguments.
type localASRInitializer struct {
	ProfileID       string `json:"profile_id"`
	ResourceVersion string `json:"resource_version"`
}

type localASRProfile struct {
	initializer localASRInitializer
	resources   []pluginruntime.ReadOnlyResource
	runner      localASRRunner
}

type localASRRunner struct {
	Python, Packages, Model, Tokens, VADModel string
	VADThreshold                              float64
	Threads                                   int
	Language                                  string
	ChunkSeconds, TimeoutSeconds              float64
}

func (m *Manager) localASRPluginResources(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) ([]pluginruntime.ReadOnlyResource, error) {
	profile, ok := m.registeredLocalASRProfile(ctx, p, g)
	if !ok {
		return nil, nil
	}
	return append([]pluginruntime.ReadOnlyResource(nil), profile.resources...), nil
}

func (m *Manager) localASRPluginMetadata(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (map[string]json.RawMessage, error) {
	profile, ok := m.registeredLocalASRProfile(ctx, p, g)
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(profile.initializer)
	if err != nil {
		return nil, errors.New("local_asr_metadata_unavailable")
	}
	return map[string]json.RawMessage{"local_asr": raw}, nil
}

// runRegisteredLocalASR runs only the verified media plugin's packaged runner
// with values resolved from the public registration. It is not a generic host
// command service: callers provide an already bound profile and PCM WAV bytes,
// not a path, executable or argv fragment.
func (m *Manager) runRegisteredLocalASR(ctx context.Context, p pluginhost.Package, g pluginhost.Grant, profile localASRProfile, wav []byte, timeoutSeconds int) (string, string, error) {
	if len(wav) == 0 || len(wav) > 10<<20 || !bytesHasWAVHeader(wav) || timeoutSeconds < 1 || timeoutSeconds > int(profile.runner.TimeoutSeconds) || int64(timeoutSeconds) > int64((1<<63-1)/int64(time.Second)) {
		return "", "", errors.New("local_asr_request_invalid")
	}
	store, err := m.packageStore()
	if err != nil {
		return "", "", err
	}
	installed, err := store.Installed(ctx, p.ID)
	if err != nil || !installed.Enabled || installed.Removed || installed.Package.Version != p.Version || installed.Package.ID != localASRPluginID {
		return "", "", errors.New("local_asr_plugin_changed")
	}
	entryName := "runtime/local_asr.py"
	if _, ok := installed.Package.Files[entryName]; !ok {
		return "", "", errors.New("local_asr_runner_unavailable")
	}
	packageDir, err := store.PackageDirectory(installed.Digest)
	if err != nil {
		return "", "", err
	}
	entry, err := cleanLocalASRPath(filepath.Join(packageDir, filepath.FromSlash(entryName)), false)
	if err != nil || hashLocalASRFile(entry) != installed.Package.Files[entryName] {
		return "", "", errors.New("local_asr_runner_unavailable")
	}
	private := filepath.Join(m.privateWorkspaceFor(g.Account), "scratch")
	if err = ensureManaged(private); err != nil {
		return "", "", err
	}
	folder, err := os.MkdirTemp(private, "plugin-asr-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(folder)
	input := filepath.Join(folder, "input.wav")
	if err = os.WriteFile(input, wav, 0600); err != nil {
		return "", "", err
	}
	readonly := make([]string, 0, len(profile.resources)+1)
	for _, resource := range profile.resources {
		// agenthost mounts paths rather than pluginruntime descriptors; each was
		// canonicalized and manifest-bound while resolving this profile.
		readonly = append(readonly, resource.Root)
	}
	// The plugin-owned runner itself is immutable package code. Its parent is
	// narrower than the package root and contains only this runtime module.
	readonly = append(readonly, filepath.Dir(entry))
	cfg := agenthost.Defaults()
	if runtime.GOOS == "windows" {
		cfg.Windows = m.agent.config().Host.Windows
	}
	cfg.Mode, cfg.OutputBytes, cfg.TimeoutSeconds = "full", 64000, timeoutSeconds
	cfg.Roots = []string{folder}
	cfg.Isolation = &agenthost.Isolation{ReadOnly: readonly}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	argv := []string{profile.runner.Python, "-X", "utf8", entry, "--packages", profile.runner.Packages, "--model", profile.runner.Model, "--tokens", profile.runner.Tokens, "--input", input, "--threads", strconv.Itoa(profile.runner.Threads), "--language", profile.runner.Language, "--chunk-seconds", strconv.FormatFloat(profile.runner.ChunkSeconds, 'f', -1, 64)}
	if profile.runner.VADModel != "" {
		argv = append(argv, "--vad-model", profile.runner.VADModel, "--vad-threshold", strconv.FormatFloat(profile.runner.VADThreshold, 'f', -1, 64))
	}
	out, err := agenthost.RunIsolated(bounded, cfg, folder, argv)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", errors.New("local_asr_execution_failed")
	}
	var result struct {
		State    string `json:"state"`
		Text     string `json:"text"`
		Complete bool   `json:"range_complete"`
	}
	if json.Unmarshal(out, &result) != nil || !result.Complete {
		return "", "", errors.New("local_asr_response_invalid")
	}
	if result.State == "no_recognizable_speech" && strings.TrimSpace(result.Text) == "" {
		return result.State, "", nil
	}
	if result.State != "transcribed" || strings.TrimSpace(result.Text) == "" {
		return "", "", errors.New("local_asr_response_invalid")
	}
	return result.State, strings.TrimSpace(result.Text), nil
}

func bytesHasWAVHeader(wav []byte) bool {
	return len(wav) >= 12 && bytes.Equal(wav[:4], []byte("RIFF")) && bytes.Equal(wav[8:12], []byte("WAVE"))
}

func (m *Manager) registeredLocalASRProfile(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (localASRProfile, bool) {
	if err := ctx.Err(); err != nil || p.ID != localASRPluginID || g.Account == "" || g.Account != m.native.Account() || g.Plugin != p.ID || g.Version != p.Version {
		return localASRProfile{}, false
	}
	// The selected local profile belongs to the media plugin configuration, not
	// the legacy speech configuration.  In particular, an old ASRLocalModel
	// value must never cause an arbitrary public-workspace profile to be
	// mounted after the media plugin has been configured for remote ASR (or has
	// not selected a local profile at all).
	store, err := m.packageStore()
	if err != nil {
		return localASRProfile{}, false
	}
	installed, err := store.Installed(ctx, localASRPluginID)
	if err != nil || !installed.Enabled || installed.Removed || installed.Package.ID != p.ID || installed.Package.Version != p.Version {
		return localASRProfile{}, false
	}
	configuration, err := store.ConfigurationForExecution(ctx, localASRPluginID)
	if err != nil {
		return localASRProfile{}, false
	}
	var backend, id string
	if json.Unmarshal(configuration.Values["asr_backend"], &backend) != nil || backend != "local" || json.Unmarshal(configuration.Values["local_asr_profile_id"], &id) != nil || !safeLocalASRID(id) {
		return localASRProfile{}, false
	}
	profile, err := m.readRegisteredLocalASRProfile(p, g, id)
	if err != nil {
		// An absent, stale or tampered registration does not grant a broad public
		// workspace mount. The media plugin can still use its separately configured
		// remote ASR route, while its local backend remains unavailable.
		return localASRProfile{}, false
	}
	return profile, true
}

func (m *Manager) readRegisteredLocalASRProfile(p pluginhost.Package, g pluginhost.Grant, id string) (localASRProfile, error) {
	if !safeLocalASRID(id) {
		return localASRProfile{}, errors.New("local_asr_model_id_invalid")
	}
	public, err := cleanLocalASRPath(filepath.Join(m.managedRoot(), "public_workspace"), true)
	if err != nil {
		return localASRProfile{}, err
	}
	manifestPath, err := cleanLocalASRPath(filepath.Join(public, "manifests", "asr-"+id+".json"), false)
	if err != nil || !insideLocalASR(public, manifestPath) {
		return localASRProfile{}, errors.New("local_asr_manifest_unavailable")
	}
	manifestBytes, err := readBoundedLocalASRFile(manifestPath, localASRManifestLimit)
	if err != nil {
		return localASRProfile{}, errors.New("local_asr_manifest_invalid")
	}
	var manifest registeredLocalASRManifest
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.ID != "asr-"+id {
		return localASRProfile{}, errors.New("local_asr_manifest_invalid")
	}
	root, err := localASRRegisteredPath(public, manifest.Root, true)
	if err != nil {
		return localASRProfile{}, err
	}
	python, err := localASRRegisteredPath(public, manifest.Python, false)
	if err != nil {
		return localASRProfile{}, err
	}
	entry, err := localASRRegisteredPath(public, manifest.Entry, false)
	if err != nil {
		return localASRProfile{}, err
	}
	packages, err := localASRRegisteredPath(public, manifest.Packages, true)
	if err != nil {
		return localASRProfile{}, err
	}
	model, err := localASRRegisteredPath(public, manifest.Model, false)
	if err != nil || !insideLocalASR(root, model) {
		return localASRProfile{}, errors.New("local_asr_model_invalid")
	}
	tokens, err := localASRRegisteredPath(public, manifest.Tokens, false)
	if err != nil || !insideLocalASR(root, tokens) {
		return localASRProfile{}, errors.New("local_asr_tokens_invalid")
	}
	// Entry remains part of the legacy registration contract, but the plugin
	// executes its verified packaged local_asr.py instead of this public path.
	if entry == "" {
		return localASRProfile{}, errors.New("local_asr_entry_invalid")
	}
	if manifest.Threads == 0 {
		manifest.Threads = 2
	}
	if manifest.Language == "" {
		manifest.Language = "auto"
	}
	if manifest.ChunkSeconds == 0 {
		manifest.ChunkSeconds = 30
	}
	if manifest.TimeoutSeconds == 0 {
		manifest.TimeoutSeconds = 120
	}
	if manifest.Threads < 1 || manifest.ChunkSeconds <= 0 || manifest.TimeoutSeconds <= 0 || manifest.TimeoutSeconds > 3600 || !validLocalASRLanguage(manifest.Language) {
		return localASRProfile{}, errors.New("local_asr_parameters_invalid")
	}
	resources := []pluginruntime.ReadOnlyResource{}
	manifestSum := sha256.Sum256(manifestBytes)
	version := hex.EncodeToString(manifestSum[:])
	appendResource := func(id, path, kind string) error {
		for _, resource := range resources {
			if sameLocalASRPath(resource.Root, path) {
				return nil
			}
		}
		resources = append(resources, pluginruntime.ReadOnlyResource{ID: id, Root: path, Kind: kind, Account: g.Account, Plugin: localASRPluginID, PackageVersion: p.Version, ManifestPath: manifestPath, ManifestSHA256: version})
		return nil
	}
	if err := appendResource("asr-interpreter", filepath.Dir(python), ""); err != nil {
		return localASRProfile{}, err
	}
	if err := appendResource("asr-sdk", packages, ""); err != nil {
		return localASRProfile{}, err
	}
	if err := appendResource("asr-model", root, ""); err != nil {
		return localASRProfile{}, err
	}
	if err := verifyLocalASRRuntimeManifest(packages); err != nil {
		return localASRProfile{}, err
	}
	// The profile identifier is the explicit media-plugin configuration value.
	// The public manifest is still named `asr-<profile-id>.json`; exposing that
	// filename prefix in host metadata would make the runtime compare two
	// different identifiers and tempt callers to reconstruct a path-like ID.
	initializer := localASRInitializer{ProfileID: id, ResourceVersion: version}
	var vadPath string
	var vadThreshold float64
	if manifest.VADModel != "" {
		vad, err := localASRRegisteredPath(public, manifest.VADModel, false)
		if err != nil || len(manifest.VADSHA256) != 64 || !validLowerSHA256(manifest.VADSHA256) || hashLocalASRFile(vad) != manifest.VADSHA256 {
			return localASRProfile{}, errors.New("local_asr_vad_invalid")
		}
		if err := appendResource("asr-vad", vad, "file"); err != nil {
			return localASRProfile{}, err
		}
		vadPath, vadThreshold = vad, manifest.VADThreshold
		if vadThreshold == 0 {
			vadThreshold = .5
		}
	}
	runner := localASRRunner{Python: python, Packages: packages, Model: model, Tokens: tokens, Threads: manifest.Threads, Language: manifest.Language, ChunkSeconds: manifest.ChunkSeconds, TimeoutSeconds: manifest.TimeoutSeconds}
	runner.VADModel, runner.VADThreshold = vadPath, vadThreshold
	return localASRProfile{initializer: initializer, resources: resources, runner: runner}, nil
}

func safeLocalASRID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' && i > 0) {
			return false
		}
	}
	return true
}

func validLocalASRLanguage(language string) bool {
	for _, allowed := range []string{"auto", "zh", "en", "ja", "ko", "yue"} {
		if language == allowed {
			return true
		}
	}
	return false
}

func localASRRegisteredPath(public, candidate string, directory bool) (string, error) {
	path, err := cleanLocalASRPath(candidate, directory)
	if err != nil || !insideLocalASR(public, path) {
		return "", errors.New("local_asr_resource_outside_public_workspace")
	}
	return path, nil
}

func cleanLocalASRPath(path string, directory bool) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path {
		return "", errors.New("local_asr_resource_path_invalid")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("local_asr_resource_linked_or_missing")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !sameLocalASRPath(path, resolved) {
		return "", errors.New("local_asr_resource_alias")
	}
	info, err := os.Stat(path)
	if err != nil || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return "", errors.New("local_asr_resource_type_invalid")
	}
	return path, nil
}

func insideLocalASR(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func sameLocalASRPath(left, right string) bool {
	return left == right || runtime.GOOS == "windows" && strings.EqualFold(left, right)
}

func readBoundedLocalASRFile(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("local_asr_file_invalid")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("local_asr_file_invalid")
	}
	return data, nil
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hashLocalASRFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func verifyLocalASRRuntimeManifest(packages string) error {
	path := filepath.Join(packages, localASRRuntimeManifest)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil // Linux registrations predate this optional Windows SDK record.
	}
	path, err := cleanLocalASRPath(path, false)
	if err != nil {
		return errors.New("local_asr_sdk_manifest_invalid")
	}
	raw, err := readBoundedLocalASRFile(path, 1<<20)
	if err != nil {
		return errors.New("local_asr_sdk_manifest_invalid")
	}
	var record struct {
		Files map[string]string `json:"files"`
	}
	if json.Unmarshal(raw, &record) != nil || len(record.Files) == 0 || len(record.Files) > 4096 {
		return errors.New("local_asr_sdk_manifest_invalid")
	}
	for relative, digest := range record.Files {
		if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.ContainsAny(relative, `:\`) || !validLowerSHA256(digest) {
			return errors.New("local_asr_sdk_manifest_invalid")
		}
		file, err := cleanLocalASRPath(filepath.Join(packages, relative), false)
		if err != nil || !insideLocalASR(packages, file) || hashLocalASRFile(file) != digest {
			return errors.New("local_asr_sdk_content_changed")
		}
	}
	return nil
}
