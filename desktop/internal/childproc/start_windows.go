package childproc

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Start hides the console and makes closing the job terminate descendants.
func Start(cmd *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	closeJob := func() { _ = windows.CloseHandle(job) }
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		closeJob()
		return nil, err
	}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			// The first process can still be between Start and assignment.
			jobErr := windows.TerminateJobObject(job, 1)
			procErr := cmd.Process.Kill()
			if jobErr == nil || procErr == nil {
				return nil
			}
			if procErr == os.ErrProcessDone {
				return os.ErrProcessDone
			}
			return procErr
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		closeJob()
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		_ = windows.CloseHandle(process)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		closeJob()
		_ = cmd.Wait()
		return nil, err
	}
	return func() {
		// Wait has joined exec's cancellation callback before the handle is closed.
		closeJob()
	}, nil
}
