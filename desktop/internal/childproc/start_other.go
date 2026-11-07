//go:build !windows

package childproc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func Start(cmd *exec.Cmd) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
