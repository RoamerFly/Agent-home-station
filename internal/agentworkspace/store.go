// Package agentworkspace stores human-readable agent notes independently from
// chat transcripts, credentials, receipts and executable task state.
package agentworkspace

import (
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
)

type Store struct {
	root string
	mu   sync.Mutex
}
type Document struct {
	Name     string `json:"name"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

var ErrConflict = errors.New("文档已被其他操作修改，请重新读取后合并；没有覆盖")

func Open(root string) (*Store, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	return &Store{root: root}, nil
}
func validScope(s string) bool {
	return s == "shared" || len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}
func validName(s string) bool {
	return len(s) > 3 && len(s) <= 100 && strings.HasSuffix(s, ".md") && !strings.ContainsAny(s, "/\\:\x00") && s != "..md"
}
func (s *Store) directory(scope string) (*os.Root, string, error) {
	if !validScope(scope) {
		return nil, "", errors.New("工作区范围无效")
	}
	path := filepath.Join(s.root, scope)
	if e := os.Mkdir(path, 0700); e != nil && !os.IsExist(e) {
		return nil, "", e
	}
	stat, e := os.Lstat(path)
	if e != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.New("工作区目录不可用")
	}
	root, e := os.OpenRoot(path)
	return root, path, e
}
func revision(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func read(root *os.Root, name string) (Document, error) {
	d := Document{Name: name}
	if !validName(name) {
		return d, errors.New("文档名须为单个 Markdown 文件名")
	}
	f, e := root.Open(name)
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
	f, e := root.Open(".")
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
	root, path, e := s.directory(scope)
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
	f, e := os.CreateTemp(path, ".document-*")
	if e != nil {
		return old, e
	}
	defer os.Remove(f.Name())
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
	if e = os.Rename(f.Name(), filepath.Join(path, name)); e != nil {
		return old, e
	}
	return Document{Name: name, Content: content, Revision: revision([]byte(content))}, nil
}
