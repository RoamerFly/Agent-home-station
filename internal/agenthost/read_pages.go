package agenthost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.Reader.Read(b)
}

func (h Host) readTextPage(ctx context.Context, f *os.File, stat os.FileInfo, in Input) (any, error) {
	if in.Offset < 0 || int64(in.Offset) > stat.Size() || in.Length < 0 {
		return nil, errors.New("读取范围无效")
	}
	hash := sha256.New()
	if _, e := io.Copy(hash, contextReader{ctx, f}); e != nil {
		return nil, e
	}
	if in.Expected != "" && in.Expected != hex.EncodeToString(hash.Sum(nil)) {
		return nil, errors.New("文件版本已变化，请从当前版本重新读取")
	}
	if _, e := f.Seek(int64(in.Offset), io.SeekStart); e != nil {
		return nil, e
	}
	length := in.Length
	if length == 0 {
		length = h.Config.OutputBytes
	}
	length = min(length, h.Config.OutputBytes)
	data, e := io.ReadAll(io.LimitReader(f, int64(length)))
	if e != nil {
		return nil, e
	}
	// Preserve UTF-8 across byte pages. No tail bytes are silently skipped.
	end := int64(in.Offset) + int64(len(data))
	if end < stat.Size() {
		for len(data) > 0 && !utf8.Valid(data) && utf8.RuneStart(data[len(data)-1]) == false {
			data = data[:len(data)-1]
		}
		if len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		end = int64(in.Offset) + int64(len(data))
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return nil, errors.New("该范围不是有效UTF-8文本；可用已授权程序读取二进制格式")
	}
	if len(data) == 0 && int64(in.Offset) < stat.Size() {
		return nil, errors.New("读取长度不足以包含一个UTF-8字符，请增大length")
	}
	after, e := f.Stat()
	if e != nil || after.Size() != stat.Size() || !after.ModTime().Equal(stat.ModTime()) {
		return nil, errors.New("读取期间文件变化，请重新读取后再编辑")
	}
	resolved, _ := filepath.EvalSymlinks(in.Path)
	return map[string]any{"path": in.Path, "resolved_path": resolved, "content": string(data), "truncated": end < stat.Size(), "complete": end == stat.Size(), "next_offset": end, "sha256": hex.EncodeToString(hash.Sum(nil)), "bytes": stat.Size()}, nil
}
