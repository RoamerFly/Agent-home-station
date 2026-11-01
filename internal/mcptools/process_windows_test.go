//go:build windows

package mcptools

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configureSessionProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000200}
}

type jobSessionProcess struct{ job windows.Handle }

// attachSessionProcess establishes the Job Object after Cmd.Start and before
// the first MCP frame.  KILL_ON_JOB_CLOSE covers helpers the bridge may spawn
// later; TerminateJobObject provides an explicit close boundary instead of
// relying on taskkill's best-effort tree enumeration.
func attachSessionProcess(cmd *exec.Cmd) (sessionProcess, error) {
	if cmd == nil || cmd.Process == nil {
		return nil, errors.New("MCP process unavailable")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		_ = windows.CloseHandle(process)
	}
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &jobSessionProcess{job: job}, nil
}

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION. x/sys exposes the
// query API but not this small structure; keep it local and exact to Win32.
type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func (p *jobSessionProcess) terminate() error {
	if p == nil || p.job == 0 {
		return errors.New("MCP process job unavailable")
	}
	job := p.job
	// TerminateJobObject is asynchronous. Keep the handle on timeout/query
	// failure so a later explicit close can re-check the same trusted tree.
	_ = windows.TerminateJobObject(job, 1)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var accounting jobAccounting
		err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
		if err == nil && accounting.ActiveProcesses == 0 {
			if closeErr := windows.CloseHandle(job); closeErr != nil {
				return closeErr
			}
			p.job = 0
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("MCP process job still active")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
