// Package agenthost supplies explicitly configured local-host capabilities.
// File operations use os.Root; full command mode runs as the service OS user,
// and is deliberately not presented as a sandbox.
package agenthost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Mode           string   `json:"mode"` // disabled, read, files, full
	Roots          []string `json:"roots"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	OutputBytes    int      `json:"output_bytes"`
}

func Defaults() Config {
	return Config{Mode: "disabled", Roots: []string{}, TimeoutSeconds: 60, OutputBytes: 16000}
}
func (c Config) Validate() error {
	if c.Mode != "disabled" && c.Mode != "read" && c.Mode != "files" && c.Mode != "full" {
		return errors.New("服务器权限级别无效")
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 300 || c.OutputBytes < 1024 || c.OutputBytes > 64000 || len(c.Roots) > 12 {
		return errors.New("服务器执行时限或输出上限无效")
	}
	for _, p := range c.Roots {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) {
			return errors.New("工作目录必须是服务器上的绝对规范路径")
		}
	}
	if c.Mode != "disabled" && len(c.Roots) == 0 {
		return errors.New("开放服务器能力前请至少指定一个工作目录")
	}
	return nil
}

type Input struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Command  string `json:"command"`
	Expected string `json:"expected_sha256"`
	Old      string `json:"old_text"`
	New      string `json:"new_text"`
	Offset   int    `json:"offset"`
}
type Host struct{ Config Config }

func (h Host) rooted(path string) (*os.Root, string, error) {
	if !filepath.IsAbs(path) {
		return nil, "", errors.New("路径必须是已授权目录内的绝对路径")
	}
	for _, base := range h.Config.Roots {
		rel, err := filepath.Rel(base, filepath.Clean(path))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			continue
		}
		root, err := os.OpenRoot(base)
		if err != nil {
			return nil, "", errors.New("已授权工作目录不可用")
		}
		return root, rel, nil
	}
	return nil, "", errors.New("路径不在已授权工作目录内")
}
func (h Host) Call(ctx context.Context, tool string, raw json.RawMessage) (any, error) {
	if err := h.Config.Validate(); err != nil {
		return nil, err
	}
	if h.Config.Mode == "disabled" {
		return nil, errors.New("服务器操作尚未授权")
	}
	var in Input
	if len(raw) > 70000 || json.Unmarshal(raw, &in) != nil {
		return nil, errors.New("服务器工具参数无效")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tool == "server_status" {
		return map[string]any{"os": runtime.GOOS, "architecture": runtime.GOARCH, "roots": h.Config.Roots, "mode": h.Config.Mode, "command_identity": "management service OS user; no automatic sudo"}, nil
	}
	root, rel, err := h.rooted(in.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	switch tool {
	case "server_list":
		f, err := root.Open(rel)
		if err != nil {
			return nil, errors.New("目录无法读取")
		}
		defer f.Close()
		entries, err := f.ReadDir(201)
		if err != nil && err != io.EOF {
			return nil, errors.New("目录无法读取")
		}
		items := []map[string]any{}
		for i, e := range entries {
			if i >= 200 {
				break
			}
			items = append(items, map[string]any{"name": e.Name(), "directory": e.IsDir(), "symlink": e.Type()&os.ModeSymlink != 0})
		}
		return map[string]any{"items": items, "truncated": len(entries) > 200}, nil
	case "server_read":
		f, err := root.Open(rel)
		if err != nil {
			return nil, errors.New("文件无法读取或越过目录边界")
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			return nil, errors.New("仅能读取普通文件")
		}
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return nil, errors.New("文件超过1MiB，请使用程序工具按需检查")
		}
		if in.Offset < 0 || in.Offset > len(data) {
			return nil, errors.New("读取偏移超出文件范围")
		}
		sum := sha256.Sum256(data)
		end := min(len(data), in.Offset+h.Config.OutputBytes)
		return map[string]any{"content": strings.ToValidUTF8(string(data[in.Offset:end]), "�"), "truncated": end < len(data), "next_offset": end, "sha256": hex.EncodeToString(sum[:]), "bytes": len(data)}, nil

	case "server_edit":
		return h.edit(root, rel, in)
	case "server_search":
		return h.search(root, rel, in.Content)
	case "server_write":
		if h.Config.Mode != "files" && h.Config.Mode != "full" {
			return nil, errors.New("没有文件写入权限")
		}
		if len(in.Content) > 64000 {
			return nil, errors.New("单次写入不能超过64KB")
		}
		// Only create new files: overwriting existing files needs an explicit flag
		// in a future API, rather than silently destroying user data.
		f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, errors.New("无法创建文件：路径不可用或文件已存在；未覆盖")
		}
		_, err = f.WriteString(in.Content)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return map[string]any{"created": true, "bytes": len(in.Content)}, nil
	case "server_exec":
		if h.Config.Mode != "full" {
			return nil, errors.New("没有命令执行权限")
		}
		if strings.TrimSpace(in.Command) == "" || len(in.Command) > 8000 || strings.ContainsRune(in.Command, 0) {
			return nil, errors.New("命令为空或过长")
		}
		f, err := root.Open(rel)
		if err != nil {
			return nil, errors.New("执行目录不存在或越过边界")
		}
		stat, err := f.Stat()
		f.Close()
		if err != nil || !stat.IsDir() {
			return nil, errors.New("执行路径必须是目录")
		}
		bounded, cancel := context.WithTimeout(ctx, time.Duration(h.Config.TimeoutSeconds)*time.Second)
		defer cancel()
		cmd := hostCommand(bounded, in.Path, in.Command)
		output := &limitedOutput{limit: h.Config.OutputBytes}
		cmd.Stdout = output
		cmd.Stderr = output
		err = cmd.Run()
		code := 0
		if err != nil {
			code = -1
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			}
		}
		return map[string]any{"output": strings.ToValidUTF8(string(output.data), "�"), "truncated": output.truncated, "exit_code": code, "timed_out": bounded.Err() == context.DeadlineExceeded, "cancelled": ctx.Err() != nil}, nil
	}
	return nil, errors.New("未知服务器工具")
}

type limitedOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
	keepTail  bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.keepTail {
		if len(p) >= b.limit {
			b.truncated = b.truncated || len(b.data) > 0 || len(p) > b.limit
			b.data = append(b.data[:0], p[len(p)-b.limit:]...)
			return n, nil
		}
		if excess := len(b.data) + len(p) - b.limit; excess > 0 {
			b.data = append(b.data[:0], b.data[excess:]...)
			b.truncated = true
		}
		b.data = append(b.data, p...)
		return n, nil
	}
	space := b.limit - len(b.data)
	if len(p) > space {
		p = p[:space]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
