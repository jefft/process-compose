//go:build !windows

package app

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureHookCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}

func cleanupHookCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// Also reap background work after a successful shell exit. Hooks own
		// their process group; they cannot leave a new service behind.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func hookExitSignal(err error) int {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return int(status.Signal())
		}
	}
	return 0
}
