// Package pluginruntime runs installed plugins independently of service adapters.
// It never starts a local plugin without the platform's mandatory sandbox.
package pluginruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"wechat-bot/internal/pluginhost"
)

// LaunchSpec is produced by the host's installed-package resolver, not by model
// arguments. Code/runtime mounts are read-only; private data is the sole writable
// mount. Broader capabilities require a separate platform policy implementation.
type LaunchSpec struct {
	PackageDir    string
	DataDir       string
	Python        string
	Pythonw       string
	WindowsHelper string
	RuntimeRoots  []string
	Resources     []ReadOnlyResource
	HostMetadata  map[string]json.RawMessage
	Configuration *pluginhost.RuntimeConfiguration
}

// ReadOnlyResource is registered by trusted host deployment code, never a
// plugin, model, tool argument or package configuration. The manifest binds a
// particular approved resource root to one account/plugin/version and detects
// changes to the registered manifest between registration and process launch.
// ManifestPath is host-only registration evidence; it is not required to be
// under Root and is never mounted merely because it was read for validation.
// It deliberately does not claim to verify every resource file: a parser for
// the declared manifest format must validate its indexed file hashes before
// treating model/runtime content as integrity checked.
type ReadOnlyResource struct {
	ID string
	// Root is a specific directory unless Kind is "file". File resources let
	// a profile expose one registered sidecar (such as a VAD model) without
	// granting its whole parent model directory.
	Root           string
	Kind           string
	Account        string
	Plugin         string
	PackageVersion string
	ManifestPath   string
	ManifestSHA256 string
}

type ResourceResolver func(context.Context, pluginhost.Package, pluginhost.Grant) ([]ReadOnlyResource, error)

// MetadataResolver supplies small host-owned initialization metadata. It is
// distinct from persisted plugin configuration and tool arguments, so a
// model, plugin setting or capability request cannot select host paths or
// replace registered resources.
type MetadataResolver func(context.Context, pluginhost.Package, pluginhost.Grant) (map[string]json.RawMessage, error)

type Resolver func(context.Context, pluginhost.Package, pluginhost.Grant) (LaunchSpec, error)
type Executor struct {
	Resolve Resolver
	// MaxFrameBytes is a transport memory limit, not a tool pagination default.
	// Oversized frames fail explicitly; they are never returned truncated.
	MaxFrameBytes int
}

func (e *Executor) Start(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginhost.Session, error) {
	cmd, cleanup, spec, err := e.prepareCommand(ctx, p, g)
	if err != nil {
		return nil, err
	}
	s, err := startSession(cmd, cleanup, p, g, e.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	if err = s.initializeConfigured(ctx, spec.Configuration, spec.HostMetadata); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// Both protocol sessions and host-bound streams use the same complete package,
// permission, resource and platform policy before obtaining a command.
func (e *Executor) prepareCommand(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (*exec.Cmd, func(), LaunchSpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, LaunchSpec{}, err
	}
	if e.Resolve == nil {
		return nil, nil, LaunchSpec{}, errors.New("plugin launch resolver unavailable")
	}
	c := pluginhost.Invocation{Account: g.Account, Thread: "host", Requester: "administrator", Purpose: "start installed plugin", RequestKey: "host-start"}
	if err := pluginhost.Authorize(p, g, c, p.Permissions); err != nil {
		return nil, nil, LaunchSpec{}, err
	}
	if p.Runtime.Transport != "mcp-stdio" {
		return nil, nil, LaunchSpec{}, errors.New("local executor requires mcp-stdio transport")
	}
	platform := false
	for _, name := range p.Platforms {
		if name == runtime.GOOS+"-"+runtime.GOARCH || name == runtime.GOOS {
			platform = true
		}
	}
	if !platform {
		return nil, nil, LaunchSpec{}, errors.New("plugin does not support current platform")
	}
	// Network, device and workspace permissions select per-call host proxies.
	// They never grant sockets, devices, workspace mounts or process execution
	// to this sandbox. Unsupported permissions, including direct GPU access,
	// fail closed; the host separately authorizes every proxy operation.
	for _, permission := range g.Permissions {
		if permission != "artifact.read" && permission != "artifact.write" && permission != "network" && permission != "model.invoke" && permission != "device" && permission != "workspace.read" && permission != "workspace.write" && permission != "process.execute" {
			return nil, nil, LaunchSpec{}, errors.New("plugin sandbox capability not implemented: " + permission)
		}
	}
	spec, err := e.Resolve(ctx, p, g)
	if err != nil {
		return nil, nil, LaunchSpec{}, err
	}
	if err = validateSpec(spec); err != nil {
		return nil, nil, LaunchSpec{}, err
	}
	if err = validateResources(p, g, spec); err != nil {
		return nil, nil, LaunchSpec{}, err
	}
	if err = validateHostMetadata(spec.HostMetadata); err != nil {
		return nil, nil, LaunchSpec{}, err
	}
	verified, err := pluginhost.Verify(spec.PackageDir)
	if err != nil {
		return nil, nil, LaunchSpec{}, err
	}
	if !sameVerifiedPackage(p, verified) {
		return nil, nil, LaunchSpec{}, errors.New("installed package changed before launch")
	}
	if p.Runtime.Entrypoint == "" {
		return nil, nil, LaunchSpec{}, errors.New("plugin entrypoint missing")
	}
	entry := filepath.Join(spec.PackageDir, filepath.FromSlash(p.Runtime.Entrypoint))
	if _, ok := p.Files[p.Runtime.Entrypoint]; !ok {
		return nil, nil, LaunchSpec{}, errors.New("entrypoint is not indexed")
	}
	argv := []string{entry}
	if strings.EqualFold(filepath.Ext(entry), ".py") {
		if spec.Python == "" || !inRoots(spec.Python, spec.RuntimeRoots) {
			return nil, nil, LaunchSpec{}, errors.New("approved Python runtime missing")
		}
		argv = []string{spec.Python, "-I", "-B", "-u", entry}
	}
	cmd, cleanup, err := isolatedCommand(spec, argv)
	if err != nil {
		return nil, nil, LaunchSpec{}, err
	}
	return cmd, cleanup, spec, nil
}

const maxHostMetadataBytes = 16 << 10

func validateHostMetadata(metadata map[string]json.RawMessage) error {
	total := 0
	for key, raw := range metadata {
		if !safeMetadataKey(key) || len(raw) == 0 || !json.Valid(raw) {
			return errors.New("plugin host metadata invalid")
		}
		total += len(key) + len(raw)
		if total > maxHostMetadataBytes {
			return errors.New("plugin host metadata exceeds limit")
		}
	}
	return nil
}

func safeMetadataKey(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	for i, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || (r == '_' || r == '-') && i > 0) {
			return false
		}
	}
	return true
}

// sameVerifiedPackage compares the package contract after normalizing the
// nil-versus-empty JSON collection distinction. A package's optional
// dependencies are encoded as null by in-memory fixtures but decoded as an
// empty collection by the backward-compatible Dependencies unmarshaler; that
// representation difference is not an on-disk package change.
func sameVerifiedPackage(expected, actual pluginhost.Package) bool {
	normalize := func(p pluginhost.Package) pluginhost.Package {
		if p.Platforms == nil {
			p.Platforms = []string{}
		}
		if p.Permissions == nil {
			p.Permissions = []string{}
		}
		if p.Dependencies == nil {
			p.Dependencies = pluginhost.Dependencies{}
		}
		if p.HTTPRoutes == nil {
			p.HTTPRoutes = []pluginhost.HTTPRoute{}
		}
		if p.Files == nil {
			p.Files = map[string]string{}
		}
		return p
	}
	return reflect.DeepEqual(normalize(expected), normalize(actual))
}

func inside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}
func inRoots(path string, roots []string) bool {
	for _, root := range roots {
		if inside(path, root) {
			return true
		}
	}
	return false
}
func cleanPath(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path {
		return errors.New("plugin sandbox requires a specific absolute path")
	}
	// Check ancestors as well: resolving only the final file misses junctions.
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("plugin sandbox path missing or linked")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	resolved, err := filepath.EvalSymlinks(path)
	same := resolved == path || runtime.GOOS == "windows" && strings.EqualFold(resolved, path)
	if err != nil || !same {
		return errors.New("plugin sandbox path alias forbidden")
	}
	info, err := os.Stat(path)
	if err != nil || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("plugin sandbox path type mismatch")
	}
	return nil
}
func validateSpec(s LaunchSpec) error {
	for _, path := range append([]string{s.PackageDir, s.DataDir}, s.RuntimeRoots...) {
		if err := cleanPath(path, true); err != nil {
			return err
		}
	}
	readOnlyRoots := append([]string{s.PackageDir}, s.RuntimeRoots...)
	for _, resource := range s.Resources {
		readOnlyRoots = append(readOnlyRoots, resource.Root)
	}
	for _, readOnly := range readOnlyRoots {
		if inside(s.DataDir, readOnly) || inside(readOnly, s.DataDir) {
			return errors.New("plugin writable directory overlaps code or runtime")
		}
	}
	if s.Python != "" {
		if err := cleanPath(s.Python, false); err != nil {
			return err
		}
	}
	return nil
}

func readOnlyRoots(s LaunchSpec) []string {
	roots := append([]string{s.PackageDir}, s.RuntimeRoots...)
	for _, resource := range s.Resources {
		roots = append(roots, resource.Root)
	}
	return roots
}

func validateResources(p pluginhost.Package, g pluginhost.Grant, s LaunchSpec) error {
	seen := map[string]bool{}
	for i, resource := range s.Resources {
		if resource.ID == "" || resource.Account != g.Account || resource.Plugin != p.ID || resource.PackageVersion != p.Version || len(resource.ManifestSHA256) != 64 || strings.ToLower(resource.ManifestSHA256) != resource.ManifestSHA256 {
			return errors.New("plugin resource registration invalid")
		}
		if _, err := hex.DecodeString(resource.ManifestSHA256); err != nil || !safeResourceID(resource.ID) || seen[resource.ID] {
			return errors.New("plugin resource registration invalid")
		}
		seen[resource.ID] = true
		if resource.Kind != "" && resource.Kind != "file" {
			return errors.New("plugin resource registration invalid")
		}
		resourceDirectory := resource.Kind != "file"
		if err := cleanPath(resource.Root, resourceDirectory); err != nil || cleanPath(resource.ManifestPath, false) != nil {
			return errors.New("plugin resource path unavailable")
		}
		manifest, err := readResourceManifest(resource.ManifestPath)
		if err != nil {
			return errors.New("plugin resource manifest unavailable")
		}
		sum := sha256.Sum256(manifest)
		if hex.EncodeToString(sum[:]) != resource.ManifestSHA256 {
			return errors.New("plugin resource manifest changed")
		}
		for _, earlier := range s.Resources[:i] {
			if samePath(resource.Root, earlier.Root) {
				return errors.New("plugin resource roots overlap")
			}
		}
		if inside(resource.Root, s.DataDir) || inside(s.DataDir, resource.Root) || inside(resource.Root, s.PackageDir) || inside(s.PackageDir, resource.Root) || inside(resource.ManifestPath, s.DataDir) || inside(resource.ManifestPath, s.PackageDir) {
			return errors.New("plugin resource overlaps private or package data")
		}
	}
	return nil
}

func samePath(left, right string) bool {
	return left == right || runtime.GOOS == "windows" && strings.EqualFold(left, right)
}

const maxResourceManifestBytes int64 = 1 << 20

// readResourceManifest bounds host-side parsing even when a resource file is
// replaced after path validation. This generic runtime only hashes the manifest
// bytes; resource-file hash semantics remain owned by its manifest parser.
func readResourceManifest(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxResourceManifestBytes {
		return nil, errors.New("plugin resource manifest unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("plugin resource manifest unavailable")
	}
	defer f.Close()
	contents, err := io.ReadAll(io.LimitReader(f, maxResourceManifestBytes+1))
	if err != nil || int64(len(contents)) > maxResourceManifestBytes {
		return nil, errors.New("plugin resource manifest unavailable")
	}
	return contents, nil
}

func safeResourceID(id string) bool {
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

// rawCopy prevents a caller mutating invocation evidence while a call is queued.
func rawCopy(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
