//go:build !windows

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/f1bonacc1/process-compose/src/types"
)

func TestHookReportsOnlyObservedExitSignals(t *testing.T) {
	err := exec.Command("sh", "-c", "kill -TERM $$").Run()
	if signal := hookExitSignal(err); signal != int(syscall.SIGTERM) {
		t.Fatalf("signal = %d, want %d", signal, syscall.SIGTERM)
	}
	err = exec.Command("sh", "-c", "exit 143").Run()
	if signal := hookExitSignal(err); signal != 0 {
		t.Fatalf("ordinary exit 143 fabricated signal %d", signal)
	}
	if signal := hookExitSignal(nil); signal != 0 {
		t.Fatalf("successful exit fabricated signal %d", signal)
	}
}

func TestHookTimeoutCleansUpDescendants(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	executeHook(ctx, &types.HookConfig{Command: "(sleep 0.5; printf leaked > leaked) & wait"}, *command.DefaultShellConfig(), os.Environ(), dir, map[string]any{"event": "start"}, func(string) {})
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "leaked")); !os.IsNotExist(err) {
		t.Fatalf("hook descendant survived cancellation: %v", err)
	}
}
