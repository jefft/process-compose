package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/f1bonacc1/process-compose/src/pclog"
	"github.com/f1bonacc1/process-compose/src/types"
)

func waitHookFile(t *testing.T, path string, predicate func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil && predicate(string(contents)) {
			return string(contents)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe expected contents in %s", path)
	return ""
}

func TestSlowPortHookCoalescesPendingTransitions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture")
	}
	dir := t.TempDir()
	conf := &types.ProcessConfig{
		Name: "worker", ReplicaName: "worker", WorkingDir: dir,
		OnStart: &types.HookConfig{Command: "printf blocked > blocked; while [ ! -f release ]; do sleep 0.01; done"},
	}
	p := NewProcess(withProcConf(conf), withProcState(types.NewProcessState(conf)), withShellConfig(*command.DefaultShellConfig()), withLogger(pclog.NewNilLogger()), withProcLog(pclog.NewLogBuffer(100)))
	i := p.newHookInstance(nil)
	i.start(999999)
	waitHookFile(t, filepath.Join(dir, "blocked"), func(string) bool { return true })
	// Supply discovery snapshots at the same seam used by the shared monitor.
	// The dispatcher is occupied by a real command throughout this burst.
	i.mu.Lock()
	i.onPortsChanged = &types.HookConfig{Command: `printf '%s|%s\n' "$PC_PREVIOUS_TCP_PORTS" "$PC_TCP_PORTS" >> events`}
	i.mu.Unlock()
	for port := uint16(1000); port < 2000; port++ {
		i.recordPorts(hookPorts{tcp: []uint16{port}, status: "ok"})
	}
	i.finish(0, "none", 0)
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-i.done:
	case <-time.After(5 * time.Second):
		t.Fatal("coalesced notifications did not drain")
	}
	data, err := os.ReadFile(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "|1999\n" {
		t.Fatalf("pending transitions were not coalesced: %q", data)
	}
}

func newHookRegressionRunner(t *testing.T, processes types.Processes) *ProjectRunner {
	t.Helper()
	project := &types.Project{Name: "hook-regression", ShellConfig: command.DefaultShellConfig(), Processes: processes}
	runner, err := NewProjectRunner(&ProjectOpts{project: project, isTuiOn: true, noWatch: true})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestExitHookDoesNotGateCompletedDependency(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture")
	}
	dir := t.TempDir()
	runner := newHookRegressionRunner(t, types.Processes{
		"producer": {
			Name: "producer", ReplicaName: "producer", Executable: "sh", Args: []string{"-c", "exit 0"}, WorkingDir: dir,
			OnExit: &types.HookConfig{Command: "while [ ! -f consumer ]; do sleep 0.01; done; printf drained > drained"},
		},
		"consumer": {
			Name: "consumer", ReplicaName: "consumer", Executable: "sh", Args: []string{"-c", "printf started > consumer"}, WorkingDir: dir,
			DependsOn: types.DependsOnConfig{"producer": {Condition: types.ProcessConditionCompletedSuccessfully}},
		},
	})
	finished := make(chan error, 1)
	go func() { finished <- runner.Run() }()
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "consumer"), nil, 0600); _ = runner.ShutDownProject() })
	waitHookFile(t, filepath.Join(dir, "consumer"), func(string) bool { return true })
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project did not finish after the dependent released the exit hook")
	}
	if _, err := os.Stat(filepath.Join(dir, "drained")); err != nil {
		t.Fatalf("project returned before exit hook drained: %v", err)
	}
}

func TestManualRestartPreservesHookOrderAndShutdownDrainsOldIncarnation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture")
	}
	dir := t.TempDir()
	runner := newHookRegressionRunner(t, types.Processes{
		"worker": {
			Name: "worker", ReplicaName: "worker", Executable: "sh", Args: []string{"-c", "printf '%s\\n' \"$$\" >> services; while :; do sleep 1; done"}, WorkingDir: dir,
			OnStart:        &types.HookConfig{Command: `printf 'start:%s\n' "$PC_PROCESS_PID" >> events`},
			OnExit:         &types.HookConfig{Command: `while [ ! -f release ]; do sleep 0.01; done; printf 'exit:%s\n' "$PC_PROCESS_PID" >> events`},
			ShutDownParams: types.ShutDownParams{ShutDownTimeout: 1},
		},
	})
	finished := make(chan error, 1)
	go func() { finished <- runner.Run() }()
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0600); _ = runner.ShutDownProject() })
	services := filepath.Join(dir, "services")
	waitHookFile(t, services, func(s string) bool { return len(strings.Fields(s)) == 1 })
	waitHookFile(t, filepath.Join(dir, "events"), func(s string) bool { return strings.HasPrefix(s, "start:") })
	if err := runner.RestartProcess("worker"); err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(waitHookFile(t, services, func(s string) bool { return len(strings.Fields(s)) == 2 }))
	// Replacement startup is independent of the old blocked exit command.
	data, err := os.ReadFile(filepath.Join(dir, "events"))
	if err != nil || string(data) != "start:"+pids[0]+"\n" {
		t.Fatalf("replacement hooks overtook old exit: %q, %v", data, err)
	}
	shutdown := make(chan error, 1)
	go func() { shutdown <- runner.ShutDownProject() }()
	select {
	case err := <-shutdown:
		t.Fatalf("shutdown discarded outstanding incarnation hooks: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not drain lifecycle notifications")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not finish after shutdown")
	}
	data, err = os.ReadFile(filepath.Join(dir, "events"))
	want := "start:" + pids[0] + "\nexit:" + pids[0] + "\nstart:" + pids[1] + "\nexit:" + pids[1] + "\n"
	if err != nil || string(data) != want {
		t.Fatalf("lifecycle order = %q, want %q; error=%v", data, want, err)
	}
}
