// Package agenthost supplies explicitly configured local-host capabilities.
// File operations use os.Root; commands and descendants require a system
// sandbox with server-derived mounts and no unrestricted host fallback.
package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"wechat-bot/internal/workspacefs"
)

type Config struct {
	Isolation      *Isolation     `json:"isolation,omitempty"`
	Windows        *WindowsConfig `json:"windows,omitempty"`
	Mode           string         `json:"mode"` // disabled, read, files, full
	Roots          []string       `json:"roots"`
	TimeoutSeconds int            `json:"timeout_seconds"`
	OutputBytes    int            `json:"output_bytes"`
}

func Defaults() Config {
	return Config{Mode: "disabled", Roots: []string{}, TimeoutSeconds: 60, OutputBytes: 16000}
}
func (c Config) Validate() error {
	if c.Windows != nil {
		if err := c.Windows.Validate(); err != nil {
			return err
		}
	}
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
	Path           string    `json:"path"`
	Content        string    `json:"content"`
	Command        string    `json:"command"`
	Expected       string    `json:"expected_sha256"`
	Old            string    `json:"old_text"`
	New            string    `json:"new_text"`
	Offset         int       `json:"offset"`
	Limit          int       `json:"limit"`
	Length         int       `json:"length"`
	Cursor         string    `json:"cursor,omitempty"`
	MaxFiles       *int      `json:"max_files,omitempty"`
	TimeoutSeconds *float64  `json:"timeout_seconds,omitempty"`
	ExcludeDirs    *[]string `json:"exclude_dirs,omitempty"`
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
		devices := []string{}
		for _, path := range []string{"/dev/dxg", "/dev/nvidia0"} {
			if info, e := os.Stat(path); e == nil && info.Mode()&os.ModeDevice != 0 {
				devices = append(devices, path)
			}
		}
		return map[string]any{"os": runtime.GOOS, "architecture": runtime.GOARCH, "roots": h.Config.Roots, "mode": h.Config.Mode, "windows_hidden_executor": h.Config.Windows != nil, "command_identity": "system-isolated workspace process; no host fallback", "isolation_required": true, "gpu_devices_available": devices, "gpu_mounted": h.Config.Isolation != nil && h.Config.Isolation.GPU, "gpu_instruction": "GPU is available to sandbox commands via gpu=true on server_exec/server_process. Use the registered CUDA runtime. cuda=False without that mount or in a CPU-only torch environment does not describe the host hardware."}, nil
	}
	if runtime.GOOS != "windows" && len(in.Path) >= 3 && in.Path[1] == ':' {
		normalized, e := WindowsLocalPath(in.Path)
		if e != nil {
			return nil, e
		}
		if stat, e := os.Stat(normalized[:6]); e != nil || !stat.IsDir() {
			return nil, errors.New("收到Windows盘符路径，但当前节点没有对应挂载；先server_status确认运行节点")
		}
		in.Path = normalized
	}
	fileHost := h
	if (tool == "server_read" || tool == "server_list" || tool == "server_search") && h.Config.Isolation != nil {
		fileHost.Config.Roots = append(append([]string{}, h.Config.Roots...), h.Config.Isolation.ReadOnly...)
	}
	root, rel, err := fileHost.rooted(in.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	switch tool {
	case "server_list":
		f, err := workspacefs.OpenDirectory(root, rel)
		if err != nil {
			return nil, errors.New("目录无法读取")
		}
		defer f.Close()
		entries, err := f.ReadDir(-1)
		if err != nil && err != io.EOF {
			return nil, errors.New("目录无法读取")
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		if in.Offset < 0 || in.Offset > len(entries) || in.Limit < 0 {
			return nil, errors.New("目录分页参数无效")
		}
		limit := in.Limit
		if limit == 0 {
			limit = 200
		}
		items := []map[string]any{}
		next := in.Offset
		used := 0
		for _, e := range entries[in.Offset:] {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			cost := len(e.Name()) + 80
			if len(items) > 0 && (len(items) >= limit || used+cost > h.Config.OutputBytes) {
				break
			}
			used += cost
			items = append(items, map[string]any{"name": e.Name(), "directory": e.IsDir(), "symlink": e.Type()&os.ModeSymlink != 0})
			next++
		}
		return map[string]any{"items": items, "truncated": next < len(entries), "complete": next == len(entries), "next_offset": next, "total": len(entries)}, nil
	case "server_read":
		f, err := workspacefs.OpenRegular(root, rel)
		if err != nil {
			return nil, errors.New("文件无法读取或越过目录边界")
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			return nil, errors.New("仅能读取普通文件")
		}
		return h.readTextPage(ctx, f, stat, in)

	case "server_edit":
		return h.edit(root, rel, in)
	case "server_search":
		return h.searchPage(ctx, root, rel, in)
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
			if errors.Is(err, os.ErrExist) {
				return nil, errors.New("文件已存在，未覆盖；先server_read再用server_edit按sha256修改")
			}
			if errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("父目录不存在；先检查路径或使用server_exec创建所需目录")
			}
			return nil, errors.New("文件创建失败；请检查目录权限与磁盘空间，未覆盖已有文件")
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
		f, err := workspacefs.OpenDirectory(root, rel)
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
		cmd, err := isolatedCommand(bounded, h.Config, in.Path, in.Command)
		if err != nil {
			return nil, err
		}
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
