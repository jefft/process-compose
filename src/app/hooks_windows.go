//go:build windows

package app

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

func configureHookCommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		cleanupHookCommand(cmd)
		return cmd.Process.Kill()
	}
}

func cleanupHookCommand(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}

func hookExitSignal(error) int {
	return 0
}
