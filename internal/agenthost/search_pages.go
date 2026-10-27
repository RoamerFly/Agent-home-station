package agenthost

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func (h Host) search(root *os.Root, rel, query string) (any, error) {
	return h.searchPage(context.Background(), root, rel, Input{Content: query})
}
func (h Host) searchPage(ctx context.Context, root *os.Root, rel string, in Input) (any, error) {
	if strings.TrimSpace(in.Content) == "" || in.Offset < 0 || in.Limit < 0 {
		return nil, errors.New("搜索文本或分页参数无效")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 100
	}
	hits := []map[string]any{}
	files, seen, used, skipped := 0, 0, 0, 0
	more := false
	query := strings.ToLower(in.Content)
	err := fs.WalkDir(root.FS(), filepath.ToSlash(rel), func(path string, entry fs.DirEntry, e error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e != nil {
			skipped++
			return nil
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
		f, e := root.Open(filepath.FromSlash(path))
		if e != nil {
			skipped++
			return nil
		}
		defer f.Close()
		files++
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		lineNo := 0
		for scanner.Scan() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lineNo++
			line := scanner.Text()
			if !utf8.ValidString(line) || strings.ContainsRune(line, 0) {
				skipped++
				return nil
			}
			if !strings.Contains(strings.ToLower(line), query) {
				continue
			}
			if seen < in.Offset {
				seen++
				continue
			}
			runes := []rune(line)
			if len(runes) > 400 {
				runes = runes[:400]
			}
			text := string(runes)
			cost := len(path) + len(text) + 80
			if len(hits) > 0 && (len(hits) >= limit || used+cost > h.Config.OutputBytes) {
				more = true
				return fs.SkipAll
			}
			used += cost
			seen++
			hits = append(hits, map[string]any{"path": filepath.Join(root.Name(), filepath.FromSlash(path)), "line": lineNo, "text": text, "excerpt_truncated": len([]rune(line)) > 400})
		}
		if scanner.Err() != nil {
			skipped++
		}
		return nil
	})
	return map[string]any{"matches": hits, "files_examined": files, "skipped_files": skipped, "truncated": more, "complete": !more && skipped == 0, "next_offset": in.Offset + len(hits), "has_more": more, "note": "offset counts matching lines in lexical file order; a changed directory is a new live search. Skipped files may need server_read or a program-specific reader."}, err
}
