package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/workspacefs"
)

func (s *localProjectTools) comfyFiles(ctx context.Context, fish string, raw json.RawMessage) (string, error) {
	var in struct {
		Action   string `json:"action"`
		Root     string `json:"root"`
		Path     string `json:"path"`
		Offset   int64  `json:"offset"`
		Limit    int64  `json:"limit"`
		Content  string `json:"content"`
		Expected string `json:"expected_sha256"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || in.Offset < 0 || in.Limit < 0 || in.Path == ".." || filepath.IsAbs(in.Path) || strings.HasPrefix(in.Path, ".."+string(os.PathSeparator)) || strings.ContainsRune(in.Path, 0) || (in.Path != "" && filepath.Clean(in.Path) != in.Path) {
		return "", errors.New("ComfyUI 文件参数无效")
	}
	view, err := s.readComfyDeployment()
	if err != nil || !view.Configured || view.Configuration == nil {
		return "", errors.New("ComfyUI 部署不可用")
	}
	r := view.Configuration
	record, err := s.home.identity.Read()
	if err != nil || record.FishID != fish || record.LocalOwner == nil {
		return "", errors.New("ComfyUI 家目录身份不可用")
	}
	home, err := s.home.directory(record)
	if err != nil {
		return "", err
	}
	areas := map[string]string{"runtime": r.Runtime, "models": r.Models, "model_library": r.ModelLibrary, "custom_nodes": r.CustomNodes, "workflows": r.Workflows, "home": home}
	base := areas[in.Root]
	if base == "" {
		return "", errors.New("ComfyUI 目录未登记")
	}
	root, err := workspacefs.OpenPath(base)
	if err != nil {
		return "", err
	}
	defer root.Close()
	rel := in.Path
	if rel == "" {
		rel = "."
	}
	switch in.Action {
	case "list":
		dir, err := workspacefs.OpenExistingDirectory(root, rel)
		if err != nil {
			return "", err
		}
		defer dir.Close()
		f, err := workspacefs.OpenDirectory(dir, ".")
		if err != nil {
			return "", err
		}
		entries, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return "", err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		if in.Limit == 0 {
			in.Limit = 100
		}
		start := min(int64(len(entries)), in.Offset)
		end := min(int64(len(entries)), start+in.Limit)
		rows := make([]map[string]any, 0, end-start)
		for _, e := range entries[start:end] {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			info, err := dir.Lstat(e.Name())
			if err != nil {
				return "", err
			}
			rows = append(rows, map[string]any{"name": e.Name(), "directory": info.IsDir(), "bytes": info.Size()})
		}
		return localProcessJSON(map[string]any{"root": in.Root, "path": in.Path, "items": rows, "total": len(entries), "next_offset": end, "has_more": end < int64(len(entries))})
	case "read":
		if rel == "." {
			return "", errors.New("请选择文件")
		}
		f, err := workspacefs.OpenRegular(root, rel)
		if err != nil {
			return "", err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return "", err
		}
		digest := ""
		if info.Size() <= 8<<20 {
			h := sha256.New()
			if _, err := io.Copy(h, f); err != nil {
				return "", err
			}
			digest = hex.EncodeToString(h.Sum(nil))
		}
		if in.Limit == 0 {
			in.Limit = 65536
		}
		effective := min(in.Limit, int64(1<<20))
		if _, err = f.Seek(in.Offset, io.SeekStart); err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(f, effective))
		if err != nil {
			return "", err
		}
		if !utf8.Valid(data) {
			return localProcessJSON(map[string]any{"root": in.Root, "path": in.Path, "bytes": info.Size(), "binary": true})
		}
		return localProcessJSON(map[string]any{"root": in.Root, "path": in.Path, "content": string(data), "bytes": info.Size(), "sha256": digest, "effective_limit": effective, "next_offset": in.Offset + int64(len(data)), "has_more": in.Offset+int64(len(data)) < info.Size()})
	case "write":
		if rel == "." || !utf8.ValidString(in.Content) || len(in.Content) > 4<<20 {
			return "", errors.New("ComfyUI 文本文件内容无效或超过 4 MiB")
		}
		parent, err := workspacefs.EnsureDirectory(root, filepath.Dir(rel))
		if err != nil {
			return "", err
		}
		defer parent.Close()
		name := filepath.Base(rel)
		if old, err := workspacefs.OpenRegular(parent, name); err == nil {
			data, readErr := io.ReadAll(io.LimitReader(old, 8<<20))
			old.Close()
			sum := sha256.Sum256(data)
			if readErr != nil || !strings.EqualFold(in.Expected, hex.EncodeToString(sum[:])) {
				return "", errors.New("文件已存在，请先读取并传入 expected_sha256")
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		tmp := ".comfy-write-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		f, err := parent.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "", err
		}
		_, err = f.WriteString(in.Content)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			parent.Remove(tmp)
			return "", err
		}
		if err = parent.Rename(tmp, name); err != nil {
			parent.Remove(tmp)
			return "", err
		}
		sum := sha256.Sum256([]byte(in.Content))
		return localProcessJSON(map[string]any{"root": in.Root, "path": in.Path, "bytes": len(in.Content), "sha256": hex.EncodeToString(sum[:])})
	}
	return "", errors.New("ComfyUI 文件操作无效")
}
