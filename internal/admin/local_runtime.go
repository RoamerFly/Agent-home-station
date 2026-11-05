package admin

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"wechat-bot/internal/agenthost"
)

// LocalWindowsRuntime uses only the packaged broker layout. It does not search
// the user's PATH or expose the package/data parent as an Agent mount.
func LocalWindowsRuntime(packageDir, privateDir, configFile string) (*agenthost.WindowsConfig, error) {
	var cfg agenthost.WindowsConfig
	if configFile != "" {
		if !filepath.IsAbs(configFile) {
			return nil, errors.New("Windows 运行器配置需要绝对路径")
		}
		file, err := os.Open(configFile)
		if err != nil {
			return nil, errors.New("Windows 运行器配置不可读")
		}
		defer file.Close()
		decoder := json.NewDecoder(io.LimitReader(file, 65537))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&cfg); err != nil {
			return nil, errors.New("Windows 运行器配置无效")
		}
		if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, errors.New("Windows 运行器配置不能包含多份文档")
		}
	} else {
		cfg.Pythonw = filepath.Join(packageDir, "runtimes", "windows-python-cli", "pythonw.exe")
		cfg.Helper = filepath.Join(packageDir, "runtimes", "windows-agent-host", "windows_agent_host.pyw")
		for _, path := range []string{cfg.Pythonw, cfg.Helper} {
			if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
				return nil, nil
			}
		}
	}
	// Execution control is never placed in an editable Agent directory, even if
	// an old runtime document supplies a spool path.
	cfg.Spool = filepath.Join(privateDir, "local-execution-spool")
	if err := cfg.Validate(); err != nil {
		return nil, errors.New("Windows 运行器路径配置无效")
	}
	for _, path := range []string{cfg.Pythonw, cfg.Helper} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("Windows 运行器文件缺失或不是普通文件")
		}
		resolved, err := filepath.EvalSymlinks(path)
		rel, relErr := filepath.Rel(path, resolved)
		if err != nil || relErr != nil || rel != "." {
			return nil, errors.New("Windows 运行器路径不能经过链接")
		}
	}
	return &cfg, nil
}
