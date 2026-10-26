package agenthost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

var fileEditMu sync.Mutex

func (h Host) edit(root *os.Root, rel string, in Input) (any, error) {
	if h.Config.Mode != "files" && h.Config.Mode != "full" {
		return nil, errors.New("没有文件修改权限")
	}
	if len(in.Expected) != 64 || in.Old == "" || len(in.New) > 64000 {
		return nil, errors.New("修改需要读取时的完整sha256、唯一旧文本和新文本")
	}
	fileEditMu.Lock()
	defer fileEditMu.Unlock()
	resolved, e := filepath.EvalSymlinks(in.Path)
	if e != nil || filepath.Clean(resolved) != filepath.Clean(in.Path) {
		return nil, errors.New("修改路径不能经过符号链接")
	}
	f, e := root.Open(rel)
	if e != nil {
		return nil, e
	}
	stat, e := f.Stat()
	if e != nil || !stat.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("仅能修改普通文本文件")
	}
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	f.Close()
	if e != nil || len(b) > 1<<20 || !utf8.Valid(b) {
		return nil, errors.New("修改文件须为不超过1MiB的UTF-8文本")
	}
	old := sha256.Sum256(b)
	if hex.EncodeToString(old[:]) != in.Expected {
		return nil, errors.New("文件已变化，请重新读取并合并；未修改")
	}
	if strings.Count(string(b), in.Old) != 1 {
		return nil, errors.New("旧文本未找到或出现多次，请提供唯一上下文；未修改")
	}
	next := strings.Replace(string(b), in.Old, in.New, 1)
	if len(next) > 1<<20 {
		return nil, errors.New("修改结果超过1MiB")
	}
	pending, e := os.CreateTemp(filepath.Dir(in.Path), ".agent-edit-*")
	if e != nil {
		return nil, e
	}
	defer os.Remove(pending.Name())
	e = pending.Chmod(stat.Mode().Perm())
	if e == nil {
		_, e = pending.WriteString(next)
	}
	if e == nil {
		e = pending.Sync()
	}
	closeErr := pending.Close()
	if e != nil {
		return nil, e
	}
	if closeErr != nil {
		return nil, closeErr
	}
	// Recheck immediately before replacing; shell tools are not serialized by
	// this lock. A later external write can still race, so use Git for repairs.
	check, e := root.Open(rel)
	if e != nil {
		return nil, e
	}
	current, e := io.ReadAll(io.LimitReader(check, (1<<20)+1))
	check.Close()
	if e != nil || string(current) != string(b) {
		return nil, errors.New("文件在修改期间变化；未覆盖")
	}
	if e = os.Rename(pending.Name(), in.Path); e != nil {
		return nil, e
	}
	sum := sha256.Sum256([]byte(next))
	return map[string]any{"updated": true, "sha256": hex.EncodeToString(sum[:]), "bytes": len(next)}, nil
}
func (h Host) search(root *os.Root, rel, query string) (any, error) {
	if strings.TrimSpace(query) == "" || len(query) > 200 {
		return nil, errors.New("搜索文本须为1至200字节")
	}
	hits := []map[string]any{}
	files := 0
	used := 0
	truncated := false
	e := fs.WalkDir(root.FS(), filepath.ToSlash(rel), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		files++
		if files > 2000 || len(hits) >= 100 {
			truncated = true
			return fs.SkipAll
		}
		f, e := root.Open(filepath.FromSlash(path))
		if e != nil {
			return nil
		}
		b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		f.Close()
		if e != nil || len(b) > 1<<20 || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
				if len(line) > 400 {
					line = line[:400]
				}
				used += len(path) + len(line) + 48
				if used > h.Config.OutputBytes {
					truncated = true
					return fs.SkipAll
				}
				hits = append(hits, map[string]any{"path": filepath.Join(root.Name(), filepath.FromSlash(path)), "line": i + 1, "text": line})
				if len(hits) >= 100 {
					truncated = true
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	return map[string]any{"matches": hits, "files_examined": min(files, 2000), "truncated": truncated}, e
}
