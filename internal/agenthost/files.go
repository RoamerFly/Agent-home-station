package agenthost

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
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
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	pendingName := filepath.Join(filepath.Dir(rel), ".agent-edit-"+hex.EncodeToString(nonce))
	pending, e := root.OpenFile(pendingName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	defer root.Remove(pendingName)
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
	if e = root.Rename(pendingName, rel); e != nil {
		return nil, e
	}
	sum := sha256.Sum256([]byte(next))
	return map[string]any{"updated": true, "sha256": hex.EncodeToString(sum[:]), "bytes": len(next)}, nil
}
