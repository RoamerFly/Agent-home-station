// Package pluginhost owns package contracts independently of service providers.
package pluginhost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const Protocol = 1

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// Package is distinct from the legacy HTTP tool manifest. A descriptor is not
// permission to install, execute or send a message.
type Package struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	HostProtocol  int               `json:"host_protocol"`
	Status        string            `json:"status"`
	Platforms     []string          `json:"platforms"`
	Runtime       Runtime           `json:"runtime"`
	Permissions   []string          `json:"permissions"`
	Dependencies  []string          `json:"dependencies"`
	HTTPRoutes    []HTTPRoute       `json:"http_routes,omitempty"`
	Files         map[string]string `json:"files"`
}
type Runtime struct {
	Transport  string `json:"transport"`
	Entrypoint string `json:"entrypoint,omitempty"`
}

func safePath(p string) bool {
	return p != "" && p != "." && fs.ValidPath(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\\:")
}

// Verify only reads a package. Every regular file except the manifest must be
// covered by its digest; links, special files and unexpected files are rejected.
// Digests prove integrity, not publisher trust. The installer must separately
// bind the verified package to an administrator-approved source.
func Verify(dir string) (Package, error) {
	var p Package
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return p, errors.New("package directory unavailable")
	}
	manifest := filepath.Join(dir, "plugin.json")
	info, err := os.Lstat(manifest)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return p, errors.New("invalid manifest file")
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return p, err
	}
	if len(raw) > 1<<20 {
		return p, errors.New("manifest exceeds 1 MiB")
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, errors.New("invalid manifest")
	}
	if p.SchemaVersion != 1 || p.HostProtocol != Protocol || !identifier.MatchString(p.ID) || strings.TrimSpace(p.Name) == "" || p.Version == "" {
		return p, errors.New("unsupported package identity or protocol")
	}
	switch p.Status {
	case "template", "migration-pending", "ready":
	default:
		return p, errors.New("invalid implementation status")
	}
	switch p.Runtime.Transport {
	case "mcp-stdio", "mcp-http", "http-adapter", "unbound":
	default:
		return p, errors.New("invalid transport")
	}
	if p.Status == "ready" && p.Runtime.Transport == "unbound" {
		return p, errors.New("ready package requires runtime")
	}
	if p.Runtime.Entrypoint != "" && !safePath(p.Runtime.Entrypoint) {
		return p, errors.New("unsafe entrypoint")
	}
	if len(p.Platforms) == 0 {
		return p, errors.New("platforms missing")
	}
	seen := map[string]bool{}
	for _, permission := range p.Permissions {
		if !allowedPermission(permission) || seen[permission] {
			return p, errors.New("unknown or repeated permission")
		}
		seen[permission] = true
	}
	for _, dependency := range p.Dependencies {
		if !identifier.MatchString(dependency) || dependency == p.ID {
			return p, errors.New("invalid dependency")
		}
	}
	if err := validateHTTPRoutes(p); err != nil {
		return p, err
	}
	for name, digest := range p.Files {
		if !safePath(name) || name == "plugin.json" || len(digest) != 64 {
			return p, errors.New("invalid file index")
		}
		if _, err = hex.DecodeString(digest); err != nil {
			return p, errors.New("invalid digest")
		}
	}
	for _, name := range []string{"config.schema.json", "ui.schema.json", "capabilities.json", "README.md", "PERMISSIONS.md", "MIGRATION.md", "CHANGELOG.md"} {
		if _, ok := p.Files[name]; !ok {
			return p, fmt.Errorf("required file missing: %s", name)
		}
	}
	found := map[string]bool{}
	err = filepath.WalkDir(dir, func(full string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("package links forbidden")
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("special file forbidden")
		}
		rel, e := filepath.Rel(dir, full)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if rel == "plugin.json" {
			return nil
		}
		expected, ok := p.Files[rel]
		if !ok {
			return fmt.Errorf("unindexed package file: %s", rel)
		}
		f, e := os.Open(full)
		if e != nil {
			return e
		}
		hash := sha256.New()
		_, e = io.Copy(hash, f)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != expected {
			return fmt.Errorf("package digest mismatch: %s", rel)
		}
		found[rel] = true
		return nil
	})
	if err != nil {
		return p, err
	}
	if len(found) != len(p.Files) {
		return p, errors.New("indexed file missing")
	}
	if p.Runtime.Entrypoint != "" && !found[p.Runtime.Entrypoint] {
		return p, errors.New("entrypoint missing")
	}
	if err := validateHTTPRouteResources(dir, p); err != nil {
		return p, err
	}
	return p, nil
}
func allowedPermission(p string) bool {
	switch p {
	case "network", "gpu", "device", "workspace.read", "workspace.write", "process.execute", "artifact.read", "artifact.write", "model.invoke":
		return true
	}
	return false
}
