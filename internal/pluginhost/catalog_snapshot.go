package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func snapshotCatalog(ctx context.Context, source, destination, digest string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat("plugin.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("invalid catalog manifest")
	}
	raw, err := root.ReadFile("plugin.json")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return errors.New("catalog manifest changed")
	}
	var p Package
	if json.Unmarshal(raw, &p) != nil || len(p.Files) == 0 {
		return errors.New("invalid catalog manifest")
	}
	if err = os.WriteFile(filepath.Join(destination, "plugin.json"), raw, 0600); err != nil {
		return err
	}
	for name := range p.Files {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !safePath(name) || name == "plugin.json" {
			return errors.New("invalid catalog member")
		}
		parts := strings.Split(name, "/")
		for i := range parts {
			st, e := root.Lstat(strings.Join(parts[:i+1], "/"))
			if e != nil || st.Mode()&os.ModeSymlink != 0 {
				return errors.New("catalog member link or missing")
			}
		}
		input, e := root.Open(name)
		if e != nil {
			return e
		}
		st, e := input.Stat()
		if e != nil || !st.Mode().IsRegular() {
			input.Close()
			return errors.New("invalid catalog member")
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			input.Close()
			return e
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			input.Close()
			return e
		}
		_, e = io.Copy(out, input)
		input.Close()
		closeErr := out.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
