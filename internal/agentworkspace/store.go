// Package agentworkspace stores human-readable agent notes independently from
// chat transcripts, credentials, receipts and executable task state.
package agentworkspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
	"wechat-bot/internal/workspacefs"
)

type Store struct {
	root             string
	ResolveDirectory func(string) (string, error)
	// BoundaryRoot is assigned by the service, never by note/tool content.
	BoundaryRoot string
	boundary     *os.Root
	boundaryPath string
	closed       bool
	mu           sync.Mutex
}
type Document struct {
	Name     string `json:"name"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

var ErrConflict = errors.New("文档已被其他操作修改，请重新读取后合并；没有覆盖")

func Open(root string) (*Store, error) {
	abs, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	dir, e := workspacefs.EnsurePath(abs)
	if e != nil {
		return nil, e
	}
	dir.Close()
	return &Store{root: abs, BoundaryRoot: abs}, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.boundary != nil {
		err := s.boundary.Close()
		s.boundary = nil
		return err
	}
	return nil
}
func validScope(s string) bool {
	return s == "shared" || len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}
func validName(s string) bool {
	return len(s) > 3 && len(s) <= 100 && strings.HasSuffix(s, ".md") && !strings.ContainsAny(s, "/\\:\x00") && s != "..md"
}
func (s *Store) directory(scope string) (*os.Root, string, error) {
	if s.closed {
		return nil, "", os.ErrClosed
	}
	if !validScope(scope) {
		return nil, "", errors.New("工作区范围无效")
	}
	path := filepath.Join(s.root, scope)
	if s.ResolveDirectory != nil {
		var e error
		path, e = s.ResolveDirectory(scope)
		if e != nil {
			return nil, "", e
		}
	}
	base := s.BoundaryRoot
	if base == "" {
		base = s.root
	}
	if s.boundary == nil || s.boundaryPath != base {
		root, e := workspacefs.EnsurePath(base)
		if e != nil {
			return nil, "", e
		}
		if s.boundary != nil {
			s.boundary.Close()
		}
		s.boundary, s.boundaryPath = root, base
	}
	rel, e := filepath.Rel(base, path)
	if e != nil {
		return nil, "", e
	}
	root, e := workspacefs.EnsureDirectory(s.boundary, rel)
	return root, path, e
}
func revision(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func read(root *os.Root, name string) (Document, error) {
	d := Document{Name: name}
	if !validName(name) {
		return d, errors.New("文档名须为单个 Markdown 文件名")
	}
	f, e := workspacefs.OpenRegular(root, name)
	if os.IsNotExist(e) {
		return d, nil
	}
	if e != nil {
		return d, e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil || !stat.Mode().IsRegular() {
		return d, errors.New("仅能读取普通文档")
	}
	b, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil {
		return d, e
	}
	if len(b) > 65536 || !utf8.Valid(b) {
		return d, errors.New("文档须为不超过64KiB的UTF-8文本")
	}
	d.Content = string(b)
	d.Revision = revision(b)
	return d, nil
}
func (s *Store) Read(scope, name string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, _, e := s.directory(scope)
	if e != nil {
		return Document{}, e
	}
	defer root.Close()
	return read(root, name)
}
func (s *Store) List(scope, query string) ([]Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, _, e := s.directory(scope)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	f, e := workspacefs.OpenDirectory(root, ".")
	if e != nil {
		return nil, e
	}
	defer f.Close()
	entries, e := f.ReadDir(-1)
	if e != nil && e != io.EOF {
		return nil, e
	}
	out := []Document{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validName(entry.Name()) {
			continue
		}
		d, e := read(root, entry.Name())
		if e != nil {
			return nil, e
		}
		if query == "" || strings.Contains(strings.ToLower(d.Name+"\n"+d.Content), strings.ToLower(query)) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// expected="" creates only a missing file. Existing files require the exact
// revision returned by Read. Interrupted writes retain the old document.
func (s *Store) Write(scope, name, content, expected string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !utf8.ValidString(content) || len(content) > 65536 {
		return Document{}, errors.New("文档须为不超过64KiB的UTF-8文本")
	}
	root, _, e := s.directory(scope)
	if e != nil {
		return Document{}, e
	}
	defer root.Close()
	old, e := read(root, name)
	if e != nil {
		return old, e
	}
	if old.Revision != expected {
		return old, ErrConflict
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return Document{}, e
	}
	pending := ".document-" + hex.EncodeToString(nonce)
	f, e := root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return old, e
	}
	defer root.Remove(pending)
	e = f.Chmod(0600)
	if e == nil {
		_, e = f.WriteString(content)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return old, e
	}
	if closeErr != nil {
		return old, closeErr
	}
	if e = root.Rename(pending, name); e != nil {
		return old, e
	}
	return Document{Name: name, Content: content, Revision: revision([]byte(content))}, nil
}
