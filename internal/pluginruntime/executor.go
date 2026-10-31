// Package pluginruntime runs installed plugins independently of service adapters.
// It never starts a local plugin without the platform's mandatory sandbox.
package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
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
	Configuration *pluginhost.RuntimeConfiguration
}
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
		if permission != "artifact.read" && permission != "artifact.write" && permission != "network" {
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
	verified, err := pluginhost.Verify(spec.PackageDir)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(p, verified) {
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
	for _, readOnly := range append([]string{s.PackageDir}, s.RuntimeRoots...) {
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

// rawCopy prevents a caller mutating invocation evidence while a call is queued.
func rawCopy(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
