package pluginhost

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Import limits are host resource boundaries, not tool pagination defaults.
// Models and environments are separate resources, not executable package files.
const (
	MaxArchiveBytes   = 64 << 20
	MaxExpandedBytes  = 256 << 20
	MaxArchiveEntries = 4096
)

var ErrArchiveLimit = errors.New("plugin archive exceeds import resource limits")

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}

// PrepareArchive accepts ZIP bytes, never extracts into the submitted filename,
// and never executes the contents. The returned token still needs Install.
func (s *Store) PrepareArchive(ctx context.Context, input io.Reader) (Prepared, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Prepared
	file, err := os.CreateTemp(s.dir, ".upload-*.zip")
	if err != nil {
		return result, err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	n, err := io.Copy(file, io.LimitReader(contextReader{ctx, input}, MaxArchiveBytes+1))
	if err != nil {
		return result, err
	}
	if n > MaxArchiveBytes {
		return result, ErrArchiveLimit
	}
	z, err := zip.NewReader(file, n)
	if err != nil {
		return result, errors.New("invalid plugin ZIP archive")
	}
	stage, err := os.MkdirTemp(s.dir, ".extract-")
	if err != nil {
		return result, err
	}
	// Newly created private child of the store; never derived from archive paths.
	defer os.RemoveAll(stage)
	root, err := extractArchive(ctx, z, stage)
	if err != nil {
		return result, err
	}
	return s.prepareStage(ctx, root)
}

// Reject aliases even on Linux so a package cannot change meaning when copied
// to Windows (ADS, device names, trailing dots/spaces and case-fold collisions).
func portableArchivePath(name string) bool {
	if !safePath(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimRight(part, " .") != part {
			return false
		}
		for _, c := range part {
			if c < 32 || strings.ContainsRune(`<>"|?*`, c) {
				return false
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			len([]rune(base)) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("123456789¹²³", []rune(base)[3]) {
			return false
		}
	}
	return true
}

func extractArchive(ctx context.Context, z *zip.Reader, stage string) (string, error) {
	if len(z.File) == 0 || len(z.File) > MaxArchiveEntries {
		return "", ErrArchiveLimit
	}
	seen := map[string]bool{}
	paths := map[string]string{}
	directories := map[string]bool{}
	var declared uint64
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := strings.TrimSuffix(f.Name, "/")
		if !portableArchivePath(name) || seen[strings.ToLower(name)] {
			return "", errors.New("unsafe or repeated archive path")
		}
		seen[strings.ToLower(name)] = true
		parts := strings.Split(name, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			key := strings.ToLower(prefix)
			dir := i < len(parts)-1 || f.Mode().IsDir()
			if original, exists := paths[key]; exists && (original != prefix || directories[key] != dir) {
				return "", errors.New("conflicting archive file or directory")
			}
			paths[key], directories[key] = prefix, dir
		}
		if !f.Mode().IsRegular() && !f.Mode().IsDir() {
			return "", errors.New("archive links and special files forbidden")
		}
		if f.UncompressedSize64 > MaxExpandedBytes-declared {
			return "", ErrArchiveLimit
		}
		declared += f.UncompressedSize64
	}
	root, err := os.OpenRoot(stage)
	if err != nil {
		return "", err
	}
	defer root.Close()
	var written int64
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := strings.TrimSuffix(f.Name, "/")
		if f.Mode().IsDir() {
			if err := root.MkdirAll(name, 0700); err != nil {
				return "", err
			}
			continue
		}
		if err := root.MkdirAll(path.Dir(name), 0700); err != nil {
			return "", err
		}
		in, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			in.Close()
			return "", err
		}
		n, copyErr := io.Copy(out, io.LimitReader(contextReader{ctx, in}, MaxExpandedBytes-written+1))
		written += n
		inErr := in.Close()
		if copyErr == nil {
			copyErr = out.Sync()
		}
		outErr := out.Close()
		if written > MaxExpandedBytes {
			return "", ErrArchiveLimit
		}
		if copyErr != nil {
			return "", copyErr
		}
		if inErr != nil {
			return "", inErr
		}
		if outErr != nil {
			return "", outErr
		}
	}
	if _, err := root.Stat("plugin.json"); err == nil {
		return stage, nil
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", err
	}
	if len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(stage, entries[0].Name()), nil
	}
	return "", errors.New("ZIP must contain plugin.json at root or inside one package folder")
}
