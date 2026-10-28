package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The WSL adapter uses a hidden Python broker and an LPAC CMD child.
// Child tokens and process jobs enforce workspace isolation without a console.
type WindowsConfig struct {
	Pythonw string `json:"pythonw"`
	Helper  string `json:"helper"`
	Spool   string `json:"spool"`
	Shell   string `json:"shell,omitempty"`
}

func (c WindowsConfig) Validate() error {
	for _, p := range []string{c.Pythonw, c.Helper, c.Spool} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) {
			return errors.New("Windows桥接需绝对规范路径：pythonw、helper、spool")
		}
	}
	if !strings.EqualFold(filepath.Base(c.Pythonw), "pythonw.exe") {
		return errors.New("隐藏Windows执行必须使用pythonw.exe，不能使用控制台python.exe")
	}
	return nil
}
func WindowsLocalPath(path string) (string, error) {
	if runtime.GOOS == "windows" {
		if filepath.IsAbs(path) {
			return filepath.Clean(path), nil
		}
	}
	if len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
		relative := filepath.Clean("/" + strings.ReplaceAll(path[3:], `\`, "/"))
		return filepath.Join("/mnt/"+strings.ToLower(path[:1]), strings.TrimPrefix(relative, "/")), nil
	}
	if strings.HasPrefix(path, "/mnt/") && len(path) >= 6 && ((path[5] >= 'a' && path[5] <= 'z') || (path[5] >= 'A' && path[5] <= 'Z')) && (len(path) == 6 || path[6] == '/') {
		clean := filepath.Clean(path)
		base := path[:6]
		if clean != base && !strings.HasPrefix(clean, base+"/") {
			return "", errors.New("映射路径不能越过当前Windows盘符根目录")
		}
		return clean, nil
	}
	return "", errors.New("Windows工作目录应为C:\\或E:\\等盘符绝对路径，或对应/mnt/c、/mnt/e路径")
}
func windowsPath(path string) (string, error) {
	if runtime.GOOS == "windows" {
		return filepath.Clean(path), nil
	}
	p, err := WindowsLocalPath(path)
	if err != nil {
		return "", err
	}
	if len(p) < 6 {
		return "", errors.New("Windows映射路径无效")
	}
	return strings.ToUpper(p[5:6]) + ":\\" + strings.ReplaceAll(strings.TrimPrefix(p[6:], "/"), "/", "\\"), nil
}

type windowsExitError struct{ code int }

func (e *windowsExitError) Error() string { return fmt.Sprintf("Windows命令退出码%d", e.code) }
func runWindowsHidden(ctx context.Context, cfg Config, id, path, command string, out io.Writer) error {
	if cfg.Isolation == nil {
		return errors.New("sandbox_required")
	}
	w := cfg.Windows
	if w == nil {
		return errors.New("Windows桥接未配置")
	}
	for _, path := range []string{w.Pythonw, w.Helper} {
		if stat, e := os.Stat(path); e != nil || !stat.Mode().IsRegular() {
			return errors.New("Windows隐藏执行器或脚本不存在；程序未启动")
		}
	}
	// Spool is control-plane data and intentionally outside Agent mounts.
	var e error
	if e = os.MkdirAll(w.Spool, 0700); e != nil {
		return errors.New("Windows桥接暂存目录不可写")
	}
	winDir, e := windowsPath(path)
	if e != nil {
		return e
	}
	dir := filepath.Join(w.Spool, id)
	if e = os.Mkdir(dir, 0700); e != nil {
		return errors.New("Windows执行记录已存在，未重放")
	}
	request := filepath.Join(dir, "request.json")
	requestWin, e := windowsPath(request)
	if e != nil {
		return e
	}
	shell := w.Shell
	if shell == "" {
		shell = `C:\Windows\System32\cmd.exe`
	}
	mounts := []string{}
	readonly := []string{}
	for _, base := range cfg.Roots {
		v, err := windowsPath(base)
		if err != nil {
			return err
		}
		mounts = append(mounts, v)
	}
	for _, base := range cfg.Isolation.ReadOnly {
		v, err := windowsPath(base)
		if err != nil {
			return err
		}
		readonly = append(readonly, v)
	}
	b, _ := json.Marshal(map[string]any{"cwd": winDir, "command": command, "shell": shell, "sandbox": true, "roots": mounts, "read_only": readonly})
	if e = os.WriteFile(request, b, 0600); e != nil {
		return e
	}
	helperWin, e := windowsPath(w.Helper)
	if e != nil {
		return e
	}
	cmd := exec.CommandContext(context.Background(), w.Pythonw, helperWin, requestWin)
	configureProcess(cmd)
	if e = cmd.Start(); e != nil {
		return fmt.Errorf("Windows隐藏执行器启动失败，请核对pythonw.exe及WSL互操作：%w", e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	resultFile := filepath.Join(dir, "result.json")
	outputFile := filepath.Join(dir, "output.log")
	var offset int64
	readOutput := func() {
		f, e := os.Open(outputFile)
		if e != nil {
			return
		}
		defer f.Close()
		if _, e = f.Seek(offset, 0); e != nil {
			return
		}
		n, _ := io.Copy(out, f)
		offset += n
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	var cancelAt time.Time
	var exitedAt time.Time
	var helperErr error
	for {
		readOutput()
		if b, e := os.ReadFile(resultFile); e == nil {
			var result struct {
				ExitCode int    `json:"exit_code"`
				Error    string `json:"error"`
			}
			if json.Unmarshal(b, &result) != nil {
				return errors.New("Windows执行结果格式无效，请核对原进程，勿重放")
			}
			if result.Error != "" {
				_, _ = fmt.Fprintln(out, result.Error)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if result.ExitCode != 0 {
				return &windowsExitError{result.ExitCode}
			}
			return nil
		}
		if !cancelAt.IsZero() && time.Since(cancelAt) > 5*time.Second {
			_ = cmd.Process.Kill()
			return ctx.Err()
		}
		if !exitedAt.IsZero() && time.Since(exitedAt) > 2*time.Second {
			return errors.New("Windows隐藏执行器已退出但未产生结果；请核对暂存记录，勿重放")
		}
		select {
		case <-ctx.Done():
			if cancelAt.IsZero() {
				cancelAt = time.Now()
				_ = os.WriteFile(filepath.Join(dir, "cancel"), nil, 0600)
			}
			// Do not spin on a permanently ready cancellation channel.
			time.Sleep(100 * time.Millisecond)
		case helperErr = <-done:
			if helperErr != nil && ctx.Err() == nil {
				return errors.New("Windows隐藏执行器异常退出；请读取暂存记录，勿重放")
			}
			done = nil
			exitedAt = time.Now()
		case <-ticker.C:
		}
	}
}
