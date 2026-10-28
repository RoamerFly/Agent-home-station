package agenthost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// The token is a continuation position, not an authorization grant. Every call
// still opens the server-authorized os.Root and validates the relative path.
type searchCursor struct {
	Binding  string `json:"binding"`
	Path     string `json:"path"`
	Byte     int64  `json:"byte"`
	Line     int    `json:"line"`
	Seen     int    `json:"seen"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	Opened   bool   `json:"opened"`
	Skipped  int    `json:"skipped"`
	Binary   int    `json:"binary"`
	Excluded int    `json:"excluded"`
}

// WalkDir orders siblings before descending, not by full path string: a/z
// precedes a.txt. Compare components to preserve that same traversal order.
func searchPathBefore(a, b string) bool {
	left, right := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < min(len(left), len(right)); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}

func (h Host) search(root *os.Root, rel, query string) (any, error) {
	return h.searchPage(context.Background(), root, rel, Input{Content: query})
}

func (h Host) searchPage(ctx context.Context, root *os.Root, rel string, in Input) (any, error) {
	if strings.TrimSpace(in.Content) == "" || in.Offset < 0 || in.Limit < 0 || (in.Cursor != "" && in.Offset != 0) {
		return nil, errors.New("搜索文本或分页参数无效；续查使用cursor，不同时传offset")
	}
	limit, maxFiles, seconds := in.Limit, 1000, 5.0
	if limit == 0 {
		limit = 100
	}
	if in.MaxFiles != nil {
		maxFiles = *in.MaxFiles
	}
	if in.TimeoutSeconds != nil {
		seconds = *in.TimeoutSeconds
	}
	if maxFiles < 0 || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return nil, errors.New("搜索分批参数必须非负且有限；0表示不设该分批边界")
	}
	excludes := []string{".git", "__pycache__", ".venv", "venv", "node_modules", "runtimes"}
	if in.ExcludeDirs != nil {
		excludes = append([]string{}, (*in.ExcludeDirs)...)
	}
	excluded := map[string]bool{}
	for _, name := range excludes {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return nil, errors.New("exclude_dirs须为目录名称；空数组表示不按名称跳过目录")
		}
		excluded[name] = true
	}
	rel = filepath.ToSlash(rel)
	query := strings.ToLower(in.Content)
	bindData, _ := json.Marshal([]any{root.Name(), rel, query, excludes})
	sum := sha256.Sum256(bindData)
	binding := hex.EncodeToString(sum[:])
	position := searchCursor{Binding: binding}
	if in.Cursor != "" {
		data, e := base64.RawURLEncoding.DecodeString(in.Cursor)
		if e != nil || json.Unmarshal(data, &position) != nil || position.Binding != binding || !fs.ValidPath(position.Path) ||
			(rel != "." && position.Path != rel && !strings.HasPrefix(position.Path, rel+"/")) || position.Byte < 0 || position.Line < 0 || position.Seen < 0 || position.Skipped < 0 || position.Binary < 0 || position.Excluded < 0 {
			return nil, errors.New("搜索cursor无效或路径、关键词、目录过滤已变化；从头重新搜索")
		}
		if !position.Opened && (position.Byte != 0 || position.Line != 0) {
			return nil, errors.New("搜索cursor缺少续查文件版本；请重新搜索")
		}
		f, e := root.Open(filepath.FromSlash(position.Path))
		if e != nil {
			return nil, errors.New("续查文件已不可用；重新搜索以取得当前目录结果")
		}
		st, e := f.Stat()
		f.Close()
		if e != nil || !st.Mode().IsRegular() || (position.Opened && (st.Size() != position.Size || st.ModTime().UnixNano() != position.Modified || position.Byte > st.Size())) {
			return nil, errors.New("续查文件已变化；重新搜索以避免混合不同版本")
		}
	}
	resume := position
	started := time.Now()
	expired := func() bool { return seconds > 0 && time.Since(started).Seconds() >= seconds }
	hits := []map[string]any{}
	files, used, skipped, binaryFiles, excludedDirs := 0, 0, resume.Skipped, resume.Binary, resume.Excluded
	seen := position.Seen
	stopReason := ""
	stop := func(reason string, at searchCursor) error {
		stopReason = reason
		at.Skipped, at.Binary, at.Excluded = skipped, binaryFiles, excludedDirs
		position = at
		return fs.SkipAll
	}
	err := fs.WalkDir(root.FS(), rel, func(path string, entry fs.DirEntry, e error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e != nil {
			skipped++
			return nil
		}
		if entry.IsDir() {
			// Do not reopen files in earlier subtrees on cursor pages.
			if resume.Path != "" && searchPathBefore(path, resume.Path) && !strings.HasPrefix(resume.Path, path+"/") && path != "." {
				return fs.SkipDir
			}
			if path != rel && excluded[entry.Name()] {
				excludedDirs++
				return fs.SkipDir
			}
			return nil
		}
		if resume.Path != "" && searchPathBefore(path, resume.Path) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		at := searchCursor{Binding: binding, Path: path, Seen: seen}
		if path == resume.Path {
			at = resume
		}
		if files > 0 && maxFiles > 0 && files >= maxFiles {
			return stop("max_files", at)
		}
		if files > 0 && expired() {
			return stop("time_slice", at)
		}
		f, e := root.Open(filepath.FromSlash(path))
		if e != nil {
			skipped++
			return nil
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() {
			skipped++
			return nil
		}
		if at.Opened && (st.Size() != at.Size || st.ModTime().UnixNano() != at.Modified) {
			return errors.New("续查文件在读取前发生变化；请重新搜索")
		}
		at.Size, at.Modified, at.Opened = st.Size(), st.ModTime().UnixNano(), true
		if _, e = f.Seek(at.Byte, 0); e != nil {
			skipped++
			return nil
		}
		files++
		// Inspect a small prefix before Scanner: a checkpoint/model must not be
		// read up to the multi-megabyte line buffer merely to discover it is binary.
		reader := bufio.NewReader(f)
		prefix, _ := reader.Peek(4096)
		if bytes.IndexByte(prefix, 0) >= 0 {
			skipped++
			binaryFiles++
			return nil
		}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		// Keep delimiters in the token so cursor byte offsets remain exact for
		// CRLF, UTF-8 and a final line without a newline.
		scanner.Split(func(data []byte, eof bool) (int, []byte, error) {
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				return i + 1, data[:i+1], nil
			}
			if eof && len(data) > 0 {
				return len(data), data, nil
			}
			return 0, nil, nil
		})
		lines := 0
		for scanner.Scan() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if lines > 0 && expired() {
				return stop("time_slice", at)
			}
			raw := scanner.Bytes()
			line := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
			if !utf8.ValidString(line) || strings.ContainsRune(line, 0) {
				skipped++
				binaryFiles++
				return nil
			}
			next := at
			next.Byte += int64(len(raw))
			next.Line++
			lines++
			if strings.Contains(strings.ToLower(line), query) {
				if seen < in.Offset {
					seen++
					next.Seen = seen
					at = next
					continue
				}
				runes := []rune(line)
				text := string(runes[:min(len(runes), 400)])
				cost := len(path) + len(text) + 80
				if len(hits) > 0 && used+cost > h.Config.OutputBytes {
					return stop("output_bytes", at)
				}
				hits = append(hits, map[string]any{"path": filepath.Join(root.Name(), filepath.FromSlash(path)), "line": next.Line, "text": text, "excerpt_truncated": len(runes) > 400})
				used += cost
				seen++
				next.Seen = seen
			}
			at = next
			if len(hits) >= limit {
				return stop("page_limit", at)
			}
		}
		if scanner.Err() != nil {
			skipped++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	more := stopReason != ""
	nextCursor := ""
	if more {
		data, _ := json.Marshal(position)
		nextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return map[string]any{
		"matches": hits, "files_examined": files, "skipped_files": skipped, "skipped_binary_files": binaryFiles,
		"excluded_directories": excludedDirs, "effective_exclude_dirs": excludes,
		"effective_max_files": maxFiles, "effective_timeout_seconds": seconds, "effective_limit": limit,
		"truncated": more, "complete": !more && skipped == 0, "coverage_complete": !more && skipped == 0 && excludedDirs == 0,
		"next_offset": seen, "next_cursor": nextCursor, "has_more": more, "stop_reason": stopReason,
		"elapsed_ms": time.Since(started).Milliseconds(),
		"note":       "Prefer next_cursor with offset omitted: resumes exact file/byte, without rereading earlier files. Keep path/content/exclude_dirs unchanged. Directory changes are live, not a snapshot. Empty page with has_more still needs continuation. Excluded directories and skipped binary/long/unreadable files are NOT searched; choose a narrower path or exclude_dirs:[] when needed. max_files/timeout_seconds are per-call slices; 0 removes that slice, cancellation still applies.",
	}, nil
}
