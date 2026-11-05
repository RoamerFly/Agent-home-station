//go:build windows

package adbserver

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type jobProcess struct {
	job  windows.Handle
	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	err  error
}

// x/sys exposes the Job query API but not this exact Win32 structure.
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

func platformStart(cmd *exec.Cmd) (Process, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	processHandle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, processHandle)
		_ = windows.CloseHandle(processHandle)
	}
	if err != nil {
		_ = windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	p := &jobProcess{job: job, cmd: cmd, done: make(chan struct{})}
	go func() { err := cmd.Wait(); p.mu.Lock(); p.err = err; p.mu.Unlock(); close(p.done) }() // exactly one child reaper
	return p, nil
}
func (p *jobProcess) Close() error {
	if p == nil || p.job == 0 {
		return nil
	}
	job := p.job
	_ = windows.TerminateJobObject(job, 1)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var info jobAccounting
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err == nil && info.ActiveProcesses == 0 {
			select {
			case <-p.done:
				p.job = 0
				return windows.CloseHandle(job)
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("adb server cleanup unconfirmed")
}

func (p *jobProcess) reaped() bool {
	if p == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *jobProcess) waitError() error { p.mu.Lock(); defer p.mu.Unlock(); return p.err }
