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
	Configuration *pluginhost.RuntimeConfiguration
}

// ReadOnlyResource is registered by trusted host deployment code, never a
// plugin, model, tool argument or package configuration. The manifest binds a
// particular approved resource root to one account/plugin/version and detects
// changes to the registered manifest between registration and process launch.
// It deliberately does not claim to verify every resource file: a parser for
// the declared manifest format must validate its indexed file hashes before
// treating model/runtime content as integrity checked.
type ReadOnlyResource struct {
	ID             string
	Root           string
	Account        string
	Plugin         string
	PackageVersion string
	ManifestPath   string
	ManifestSHA256 string
}

type ResourceResolver func(context.Context, pluginhost.Package, pluginhost.Grant) ([]ReadOnlyResource, error)
type Resolver func(context.Context, pluginhost.Package, pluginhost.Grant) (LaunchSpec, error)
type Executor struct {
	Resolve Resolver
	// MaxFrameBytes is a transport memory limit, not a tool pagination default.
	// Oversized frames fail explicitly; they are never returned truncated.
	MaxFrameBytes int
}

func (e *Executor) Start(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginhost.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.Resolve == nil {
		return nil, errors.New("plugin launch resolver unavailable")
	}
	c := pluginhost.Invocation{Account: g.Account, Thread: "host", Requester: "administrator", Purpose: "start installed plugin", RequestKey: "host-start"}
	if err := pluginhost.Authorize(p, g, c, p.Permissions); err != nil {
		return nil, err
	}
	if p.Runtime.Transport != "mcp-stdio" {
		return nil, errors.New("local executor requires mcp-stdio transport")
	}
	platform := false
	for _, name := range p.Platforms {
		if name == runtime.GOOS+"-"+runtime.GOARCH || name == runtime.GOOS {
			platform = true
		}
	}
	if !platform {
		return nil, errors.New("plugin does not support current platform")
	}
	// Network is a per-call host proxy, never a direct socket grant. The platform
	// sandbox stays offline; unsupported GPU/device/workspace grants fail closed.
	for _, permission := range g.Permissions {
		if permission != "artifact.read" && permission != "artifact.write" && permission != "network" && permission != "model.invoke" {
			return nil, errors.New("plugin sandbox capability not implemented: " + permission)
		}
	}
	spec, err := e.Resolve(ctx, p, g)
	if err != nil {
		return nil, err
	}
	if err = validateSpec(spec); err != nil {
		return nil, err
	}
	if err = validateResources(p, g, spec); err != nil {
		return nil, err
	}
	verified, err := pluginhost.Verify(spec.PackageDir)
	if err != nil {
		return nil, err
	}
	if !sameVerifiedPackage(p, verified) {
		return nil, errors.New("installed package changed before launch")
	}
	if p.Runtime.Entrypoint == "" {
		return nil, errors.New("plugin entrypoint missing")
	}
	entry := filepath.Join(spec.PackageDir, filepath.FromSlash(p.Runtime.Entrypoint))
	if _, ok := p.Files[p.Runtime.Entrypoint]; !ok {
		return nil, errors.New("entrypoint is not indexed")
	}
	argv := []string{entry}
	if strings.EqualFold(filepath.Ext(entry), ".py") {
		if spec.Python == "" || !inRoots(spec.Python, spec.RuntimeRoots) {
			return nil, errors.New("approved Python runtime missing")
		}
		argv = []string{spec.Python, "-I", "-B", "-u", entry}
	}
	cmd, cleanup, err := isolatedCommand(spec, argv)
	if err != nil {
		return nil, err
	}
	s, err := startSession(cmd, cleanup, p, g, e.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	if err = s.initializeConfigured(ctx, spec.Configuration); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
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
		if err := cleanPath(resource.Root, true); err != nil || cleanPath(resource.ManifestPath, false) != nil || !inside(resource.ManifestPath, resource.Root) {
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
			if inside(resource.Root, earlier.Root) || inside(earlier.Root, resource.Root) {
				return errors.New("plugin resource roots overlap")
			}
		}
		if inside(resource.Root, s.DataDir) || inside(s.DataDir, resource.Root) || inside(resource.Root, s.PackageDir) || inside(s.PackageDir, resource.Root) {
			return errors.New("plugin resource overlaps private or package data")
		}
	}
	return nil
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
