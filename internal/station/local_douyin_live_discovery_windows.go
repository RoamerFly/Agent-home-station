//go:build windows

package station

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"wechat-bot/internal/nativeclient"
)

const douyinCompanionExecutable = "直播伴侣.exe"

func douyinInstalledRoots() []string {
	var roots []string
	for _, hive := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			roots = append(roots, douyinUninstallRoots(hive, view)...)
		}
	}
	if executable, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Join(filepath.Dir(executable), "connected-apps", "douyin", "runtime", "live-companion"))
	}
	return roots
}

func douyinUninstallRoots(hive registry.Key, view uint32) []string {
	const subkey = `Software\Microsoft\Windows\CurrentVersion\Uninstall`
	key, err := registry.OpenKey(hive, subkey, registry.ENUMERATE_SUB_KEYS|view)
	if err != nil {
		return nil
	}
	defer key.Close()
	names, _ := key.ReadSubKeyNames(-1)
	var roots []string
	for _, name := range names {
		item, err := registry.OpenKey(key, name, registry.QUERY_VALUE|view)
		if err != nil {
			continue
		}
		label, _, _ := item.GetStringValue("DisplayName")
		location, kind, _ := item.GetStringValue("InstallLocation")
		item.Close()
		if label != "直播伴侣" && !strings.HasPrefix(label, "直播伴侣 ") {
			continue
		}
		if kind == registry.EXPAND_SZ {
			location, _ = registry.ExpandString(location)
		}
		if location = strings.TrimSpace(location); location != "" {
			roots = append(roots, filepath.Clean(location))
		}
	}
	return roots
}

func douyinLiveLocalDirectory(path string) bool {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path[2:], ":\x00") {
		return false
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil || windows.GetDriveType(root) != windows.DRIVE_FIXED {
		return false
	}
	for current := path; ; current = filepath.Dir(current) {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return false
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil || attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return false
		}
		if filepath.Dir(current) == current {
			return true
		}
	}
}

func douyinLiveVersionDirectory(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) < 3 || len(parts) > 5 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 12 || strings.Trim(part, "0123456789") != "" {
			return false
		}
	}
	return true
}

type douyinLiveInstallation struct {
	path string
	file nativeclient.Info
}

func discoverDouyinLiveCompanion(ctx context.Context, roots []string, inspect func(context.Context, string) (nativeclient.Info, error)) ([]douyinLiveInstallation, error) {
	seen := make(map[string]bool)
	var installations []douyinLiveInstallation
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !douyinLiveLocalDirectory(root) {
			continue
		}
		directory, err := os.Open(root)
		if err != nil {
			continue
		}
		entries, err := directory.ReadDir(257)
		directory.Close()
		if err != nil && !errors.Is(err, io.EOF) || len(entries) > 256 {
			continue
		}
		candidates := []string{filepath.Join(root, douyinCompanionExecutable)}
		for _, entry := range entries {
			if entry.IsDir() && douyinLiveVersionDirectory(entry.Name()) {
				candidates = append(candidates, filepath.Join(root, entry.Name(), douyinCompanionExecutable))
			}
		}
		for _, path := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			key := strings.ToLower(path)
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, err := os.Lstat(path); err != nil {
				continue
			}
			if info, err := inspect(ctx, path); err == nil {
				if role, _ := douyinLiveClientIdentity(info); role == "main" {
					installations = append(installations, douyinLiveInstallation{path: path, file: info})
				}
			}
		}
	}
	return installations, ctx.Err()
}

// A process snapshot establishes only a matching executable path at the time
// of observation. Account, capture and platform state are separate evidence.
func douyinLiveProcessState(ctx context.Context, executable string) string {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return "unavailable"
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	unknown := false
	for err := windows.Process32First(snapshot, &entry); ; err = windows.Process32Next(snapshot, &entry) {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			if unknown {
				return "unavailable"
			}
			return "not_observed"
		}
		if err != nil || ctx.Err() != nil {
			return "unavailable"
		}
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(executable)) {
			continue
		}
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if err != nil {
			unknown = true
			continue
		}
		var path [32768]uint16
		length := uint32(len(path))
		err = windows.QueryFullProcessImageName(process, 0, &path[0], &length)
		windows.CloseHandle(process)
		if err != nil || length == 0 || length >= uint32(len(path)) {
			unknown = true
			continue
		}
		if strings.EqualFold(filepath.Clean(windows.UTF16ToString(path[:length])), executable) {
			return "observed"
		}
	}
}
