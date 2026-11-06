//go:build windows

package stationbrowser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"wechat-bot/internal/workspacefs"
)

// Available only locates Edge in the two protected Program Files locations.
// It intentionally never consults PATH, registry overrides, or environment
// executable variables: those would turn browser discovery into code loading.
func Available() bool { _, err := trustedEdge(); return err == nil }
func trustedEdge() (string, error) {
	for _, p := range []string{`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, `C:\Program Files\Microsoft\Edge\Application\msedge.exe`} {
		if i, e := os.Stat(p); e == nil && !i.IsDir() {
			return p, nil
		}
	}
	return "", errCDPUnavailable
}

func openCDP(ctx context.Context, profile string) (*cdpSession, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	edge, e := trustedEdge()
	if e != nil {
		return nil, e
	}
	profile, e = filepath.Abs(profile)
	if e != nil {
		return nil, e
	}
	if filepath.Clean(profile) != profile {
		return nil, fmt.Errorf("%w: invalid private profile", errCDPProtocol)
	}
	r, e := workspacefs.EnsurePath(profile)
	if e != nil {
		return nil, fmt.Errorf("%w: private profile: %v", errCDPProtocol, e)
	}
	_ = r.Close()
	return startEdgePipe(ctx, edge, profile)
}
func startEdgePipe(ctx context.Context, edge, profile string) (*cdpSession, error) {
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var browserRead, hostWrite, hostRead, browserWrite windows.Handle
	if e := windows.CreatePipe(&browserRead, &hostWrite, sa, 0); e != nil {
		return nil, e
	}
	defer windows.CloseHandle(browserRead)
	if e := windows.CreatePipe(&hostRead, &browserWrite, sa, 0); e != nil {
		_ = windows.CloseHandle(hostWrite)
		return nil, e
	}
	defer windows.CloseHandle(browserWrite)
	if e := windows.SetHandleInformation(hostWrite, windows.HANDLE_FLAG_INHERIT, 0); e != nil {
		_ = windows.CloseHandle(hostWrite)
		_ = windows.CloseHandle(hostRead)
		return nil, e
	}
	if e := windows.SetHandleInformation(hostRead, windows.HANDLE_FLAG_INHERIT, 0); e != nil {
		_ = windows.CloseHandle(hostWrite)
		_ = windows.CloseHandle(hostRead)
		return nil, e
	}
	nul, e := windows.CreateFile(windows.StringToUTF16Ptr("NUL"), windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if e != nil {
		_ = windows.CloseHandle(hostWrite)
		_ = windows.CloseHandle(hostRead)
		return nil, e
	}
	defer windows.CloseHandle(nul)
	// The extended handle list prevents this child inheriting arbitrary handles
	// from the desktop process (including its API, login, and admin handles).
	for _, handle := range []windows.Handle{browserRead, browserWrite, nul} {
		if e = windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); e != nil {
			_ = windows.CloseHandle(hostWrite)
			_ = windows.CloseHandle(hostRead)
			return nil, e
		}
	}
	job, e := windows.CreateJobObject(nil, nil)
	if e != nil {
		_ = windows.CloseHandle(hostWrite)
		_ = windows.CloseHandle(hostRead)
		return nil, e
	}
	var lim windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	lim.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, e = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&lim)), uint32(unsafe.Sizeof(lim))); e != nil {
		_ = windows.CloseHandle(job)
		_ = windows.CloseHandle(hostWrite)
		_ = windows.CloseHandle(hostRead)
		return nil, e
	}
	args := []string{edge, "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-component-update", "--disable-sync", "--disable-extensions", "--metrics-recording-only", "--remote-debugging-pipe", fmt.Sprintf("--remote-debugging-io-pipes=%d,%d", browserRead, browserWrite), "--user-data-dir=" + profile, "about:blank"}
	cmd, e := windows.UTF16PtrFromString(joinWindowsArgs(args))
	if e != nil {
		return nil, closeStart(job, hostWrite, hostRead, e)
	}
	app, _ := windows.UTF16PtrFromString(edge)
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(edge))
	env := narrowEnvironment()
	attributes, e := windows.NewProcThreadAttributeList(1)
	if e != nil {
		return nil, closeStart(job, hostWrite, hostRead, e)
	}
	defer attributes.Delete()
	handles := []windows.Handle{browserRead, browserWrite, nul}
	if e = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); e != nil {
		return nil, closeStart(job, hostWrite, hostRead, e)
	}
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES, StdInput: nul, StdOutput: nul, StdErr: nul}, ProcThreadAttributeList: attributes.List()}
	var pi windows.ProcessInformation
	if e = windows.CreateProcess(app, cmd, nil, nil, true, windows.CREATE_SUSPENDED|windows.CREATE_NO_WINDOW|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT, &env[0], cwd, &si.StartupInfo, &pi); e != nil {
		return nil, closeStart(job, hostWrite, hostRead, e)
	}
	defer windows.CloseHandle(pi.Thread)
	if e = windows.AssignProcessToJobObject(job, pi.Process); e != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Process)
		return nil, closeStart(job, hostWrite, hostRead, e)
	}
	if _, e = windows.ResumeThread(pi.Thread); e != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(pi.Process)
		return nil, closeStart(job, hostWrite, hostRead, e)
	}
	if e = ctx.Err(); e != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(pi.Process)
		return nil, closeStart(job, hostWrite, hostRead, e)
	}
	in := os.NewFile(uintptr(hostWrite), "edge-cdp-in")
	out := os.NewFile(uintptr(hostRead), "edge-cdp-out")
	session := newCDPSession(in, out, func() error {
		_ = windows.TerminateJobObject(job, 1)
		_, _ = windows.WaitForSingleObject(pi.Process, 2000)
		_ = windows.CloseHandle(pi.Process)
		return windows.CloseHandle(job)
	})
	session.start(ctx)
	return session, nil
}
func closeStart(job, one, two windows.Handle, e error) error {
	_ = windows.CloseHandle(job)
	_ = windows.CloseHandle(one)
	_ = windows.CloseHandle(two)
	return e
}
func narrowEnvironment() []uint16 {
	names := []string{"SystemRoot", "WINDIR", "SystemDrive", "COMSPEC", "PATH", "PATHEXT", "TEMP", "TMP", "LOCALAPPDATA", "APPDATA", "PROGRAMDATA", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"}
	lines := make([]string, 0, len(names))
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok && v != "" {
			lines = append(lines, n+"="+v)
		}
	}
	sort.Strings(lines)
	// UTF16FromString forbids embedded NULs. CreateProcess requires a sequence
	// of NUL-terminated NAME=value entries followed by one extra NUL.
	return append(utf16.Encode([]rune(strings.Join(lines, "\x00"))), 0, 0)
}
func joinWindowsArgs(a []string) string {
	for i, v := range a {
		a[i] = quoteWindowsArg(v)
	}
	return strings.Join(a, " ")
}
func quoteWindowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slash := 0
	for _, r := range s {
		if r == '\\' {
			slash++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat("\\", slash*2+1))
			b.WriteRune(r)
			slash = 0
			continue
		}
		if slash > 0 {
			b.WriteString(strings.Repeat("\\", slash))
			slash = 0
		}
		b.WriteRune(r)
	}
	if slash > 0 {
		b.WriteString(strings.Repeat("\\", slash*2))
	}
	b.WriteByte('"')
	return b.String()
}
