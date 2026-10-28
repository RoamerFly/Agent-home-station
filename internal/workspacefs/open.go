// Package workspacefs opens typed entries through an already authorized root.
// It adds no file-size or execution limits. Callers own pagination and content
// validation; special files must not block the management service during open.
package workspacefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

var ErrNotRegular = errors.New("expected a regular file")
var ErrNotDirectory = errors.New("expected a directory")

func OpenRegular(root *os.Root, name string) (*os.File, error) {
	return open(root, name, false)
}

func OpenDirectory(root *os.Root, name string) (*os.File, error) {
	return open(root, name, true)
}

// FS keeps directory traversal on the same typed, nonblocking open path.
func FS(root *os.Root) fs.FS { return rootedFS{root} }

type rootedFS struct{ root *os.Root }

func (s rootedFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &os.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	path := filepath.FromSlash(name)
	info, err := s.root.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return openChecked(s.root, path, true)
	}
	if !info.Mode().IsRegular() {
		return nil, kindError(name, false)
	}
	return openChecked(s.root, path, false)
}

func kindError(name string, directory bool) error {
	err := ErrNotRegular
	if directory {
		err = ErrNotDirectory
	}
	return &os.PathError{Op: "open", Path: name, Err: err}
}

func matches(info os.FileInfo, directory bool) bool {
	if directory {
		return info.IsDir()
	}
	return info.Mode().IsRegular()
}

func open(root *os.Root, name string, directory bool) (*os.File, error) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !matches(info, directory) {
		return nil, kindError(name, directory)
	}
	return openChecked(root, name, directory)
}

func openChecked(root *os.Root, name string, directory bool) (*os.File, error) {
	// Stat is only an early filter. The entry can be replaced with a FIFO
	// before open; nonblocking open followed by fstat closes that race. Root
	// resolution also prevents a replaced symlink from escaping the grant.
	f, err := root.OpenFile(name, os.O_RDONLY|nonblockingFlag, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !matches(info, directory) {
		f.Close()
		return nil, kindError(name, directory)
	}
	return f, nil
}
