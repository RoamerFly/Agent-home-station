//go:build windows

package devicebroker

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configureIOCommand(cmd *exec.Cmd) {
	// Assign the process to its job before any executable instruction can spawn
	// a descendant. Go closes the primary thread handle, so resume the process
	// itself only after the job and cancellation state have both been checked.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 | 0x00000004}
}

func runIOCommand(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	var cancelMu sync.Mutex
	cancelled := false
	cmd.Cancel = func() error {
		cancelMu.Lock()
		defer cancelMu.Unlock()
		cancelled = true
		return windows.TerminateJobObject(job, 1)
	}
	if err = cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|0x0800, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, handle)
		if err == nil {
			cancelMu.Lock()
			if cancelled {
				err = windows.TerminateJobObject(job, 1)
			} else if status, _, _ := syscall.NewLazyDLL("ntdll.dll").NewProc("NtResumeProcess").Call(uintptr(handle)); status != 0 {
				err = errors.New("device_io_resume_unavailable")
			}
			cancelMu.Unlock()
		}
		_ = windows.CloseHandle(handle)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return err
	}
	err = cmd.Wait()
	var mu sync.Mutex
	closed := false
	cleanup := func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return nil
		}
		_ = windows.TerminateJobObject(job, 0)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			var accounting struct {
				TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
				TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
			}
			if query := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); query == nil && accounting.ActiveProcesses == 0 {
				if closeErr := windows.CloseHandle(job); closeErr != nil {
					return closeErr
				}
				closed = true
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("device_io_cleanup_unconfirmed")
	}
	if pending := cleanup(); pending != nil {
		return &ioCleanupError{err: errors.Join(err, pending), cleanup: cleanup}
	}
	return err
}
