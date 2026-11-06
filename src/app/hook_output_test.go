package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/f1bonacc1/process-compose/src/types"
)

func TestHookOutputJournalCursor(t *testing.T) {
	j := &hookOutputJournal{}
	j.append("web", "start", "first")
	j.append("web", "ready", "second")
	j.append("", "ready", "third")

	full := j.snapshot(0)
	if len(full.Lines) != 3 || full.NextSequence != 3 || full.Truncated || full.Pending != 0 || full.StartupComplete {
		t.Fatalf("unexpected snapshot: %+v", full)
	}
	if full.Lines[0].Process != "web" || full.Lines[0].Event != "start" || full.Lines[2].Process != "" {
		t.Fatalf("unexpected lines: %+v", full.Lines)
	}
	partial := j.snapshot(1)
	if len(partial.Lines) != 2 || partial.Lines[0].Message != "second" || partial.Lines[1].Sequence != 3 {
		t.Fatalf("cursor filtering failed: %+v", partial.Lines)
	}
	afterEnd := j.snapshot(3)
	if len(afterEnd.Lines) != 0 || afterEnd.Lines == nil || afterEnd.NextSequence != 3 || afterEnd.Truncated {
		t.Fatalf("cursor at end must replay nothing: %+v", afterEnd)
	}
	repeat := j.snapshot(3)
	if len(repeat.Lines) != 0 {
		t.Fatalf("repeat after cursor returned duplicates: %+v", repeat.Lines)
	}
}

func TestHookOutputJournalBoundsAndReset(t *testing.T) {
	j := &hookOutputJournal{}
	j.append("web", "start", "first")
	j.append("web", "ready", "second")
	j.append("", "ready", "third")
	partial := j.snapshot(1)

	// Later eviction must not mutate an already returned snapshot.
	for range 2000 {
		j.append("noise", "ports_changed", "x")
	}
	if len(partial.Lines) != 2 || partial.Lines[0].Message != "second" {
		t.Fatalf("returned snapshot was mutated by eviction: %+v", partial.Lines)
	}
	retained := j.snapshot(0)
	if len(retained.Lines) > hookOutputMaxLines {
		t.Fatalf("line bound exceeded: %d", len(retained.Lines))
	}
	size := 0
	for _, line := range retained.Lines {
		size += hookOutputBytes(line)
	}
	if size > hookOutputMaxBytes {
		t.Fatalf("byte bound exceeded: %d", size)
	}
	if !retained.Truncated || retained.Lines[0].Sequence <= 3 {
		t.Fatalf("evicted history not flagged: truncated=%v oldest=%d", retained.Truncated, retained.Lines[0].Sequence)
	}

	// A cursor still inside the retained range is not truncated.
	fresh := j.snapshot(retained.Lines[0].Sequence - 1)
	if fresh.Truncated || fresh.Lines[0].Sequence != retained.Lines[0].Sequence {
		t.Fatalf("live cursor flagged as truncated: %+v", fresh)
	}

	j.reset()
	if reset := j.snapshot(0); reset.NextSequence != 0 || len(reset.Lines) != 0 || reset.StartupComplete || reset.Pending != 0 || reset.Truncated {
		t.Fatalf("reset left state behind: %+v", reset)
	}
}

func TestHookOutputJournalPendingAccounting(t *testing.T) {
	j := &hookOutputJournal{}
	j.admit()
	j.admit()
	if snapshot := j.snapshot(0); snapshot.Pending != 2 {
		t.Fatalf("pending = %d, want 2", snapshot.Pending)
	}
	j.complete()
	if snapshot := j.snapshot(0); snapshot.Pending != 1 {
		t.Fatalf("pending = %d, want 1", snapshot.Pending)
	}
	j.complete()
	if snapshot := j.snapshot(0); snapshot.Pending != 0 {
		t.Fatalf("pending = %d, want 0", snapshot.Pending)
	}

	// Instances built without a journal (unit fixtures) stay no-ops.
	var missing *hookOutputJournal
	missing.admit()
	missing.complete()
	missing.startupReady()
	missing.append("a", "start", "b")
}

func TestPortCoalescingKeepsPendingCount(t *testing.T) {
	j := &hookOutputJournal{}
	i := &processHookInstance{
		journal: j, wake: make(chan struct{}, 1), started: true,
		onPortsChanged: &types.HookConfig{Command: "unused"},
	}
	i.recordPorts(hookPorts{tcp: []uint16{80}, status: "ok"})
	i.recordPorts(hookPorts{tcp: []uint16{80}, status: "ok"})
	i.recordPorts(hookPorts{tcp: []uint16{443}, status: "ok"})
	if snapshot := j.snapshot(0); snapshot.Pending != 1 || len(i.queue) != 1 {
		t.Fatalf("replacement inflated pending: pending=%d queue=%d", snapshot.Pending, len(i.queue))
	}
	// Drain the retained event the way the dispatcher does.
	i.mu.Lock()
	i.queue = i.queue[1:]
	i.mu.Unlock()
	j.append("worker", "ports_changed", "80 443")
	j.complete()
	if snapshot := j.snapshot(0); snapshot.Pending != 0 || snapshot.Lines[0].Message != "80 443" {
		t.Fatalf("drained event not accounted: %+v", snapshot)
	}

	// A new observation that cancels the pending transition releases its
	// admission without executing the coalesced notification.
	i.recordPorts(hookPorts{tcp: []uint16{8080}, status: "ok"})
	i.recordPorts(hookPorts{tcp: []uint16{443}, status: "ok"})
	if snapshot := j.snapshot(0); snapshot.Pending != 0 || len(i.queue) != 0 {
		t.Fatalf("coalesced removal left pending work: pending=%d queue=%d", snapshot.Pending, len(i.queue))
	}
}

func TestRunnerHookOutputSafeBeforeRun(t *testing.T) {
	runner := newHookRegressionRunner(t, types.Processes{
		"task": {Name: "task", ReplicaName: "task", Executable: "sh", Args: []string{"-c", "exit 0"}},
	})
	snapshot, err := runner.GetHookOutput(0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StartupComplete || snapshot.Pending != 0 || len(snapshot.Lines) != 0 || snapshot.Lines == nil || snapshot.Truncated {
		t.Fatalf("unexpected pre-Run snapshot: %+v", snapshot)
	}
}

func TestProjectWithoutHooksCompletesStartup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture")
	}
	runner := newHookRegressionRunner(t, types.Processes{
		"task": {Name: "task", ReplicaName: "task", Executable: "sh", Args: []string{"-c", "exit 0"}},
	})
	finished := make(chan error, 1)
	go func() { finished <- runner.Run() }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project did not complete")
	}
	snapshot, err := runner.GetHookOutput(0)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.StartupComplete || snapshot.Pending != 0 || len(snapshot.Lines) != 0 {
		t.Fatalf("startup not complete after run: %+v", snapshot)
	}
}

func containsHookLine(snapshot *types.HookOutputSnapshot, message string) bool {
	for _, line := range snapshot.Lines {
		if line.Message == message {
			return true
		}
	}
	return false
}

func waitForHookSnapshot(t *testing.T, runner *ProjectRunner, predicate func(*types.HookOutputSnapshot) bool) *types.HookOutputSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var snapshot *types.HookOutputSnapshot
	for time.Now().Before(deadline) {
		var err error
		snapshot, err = runner.GetHookOutput(0)
		if err == nil && predicate(snapshot) {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snapshot == nil {
		snapshot, _ = runner.GetHookOutput(0)
	}
	t.Fatalf("startup output condition not met: %+v", snapshot)
	return nil
}

func TestDelayedReadyHookBlocksStartupCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture")
	}
	dir := t.TempDir()
	runner := newHookRegressionRunner(t, types.Processes{
		"web": {
			Name: "web", ReplicaName: "web", Executable: "sh", WorkingDir: dir,
			Args:         []string{"-c", "printf start > phase1; while [ ! -f go ]; do sleep 0.01; done; echo READY; while [ ! -f release ]; do sleep 0.01; done"},
			OnStart:      &types.HookConfig{Command: "printf starting-web"},
			OnReady:      &types.HookConfig{Command: "while [ ! -f release ]; do sleep 0.01; done; printf ready-web"},
			ReadyLogLine: "READY",
		},
	})
	finished := make(chan error, 1)
	go func() { finished <- runner.Run() }()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
		_ = runner.ShutDownProject()
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("runner did not finish after shutdown")
		}
	})

	waitHookFile(t, filepath.Join(dir, "phase1"), func(string) bool { return true })
	snapshot := waitForHookSnapshot(t, runner, func(s *types.HookOutputSnapshot) bool {
		return containsHookLine(s, "starting-web")
	})
	// The ready log line cannot have been printed yet.
	if snapshot.StartupComplete {
		t.Fatalf("startup complete before readiness log: %+v", snapshot)
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Readiness admission is observable before the readiness state itself:
	// once startup is flagged complete, the blocked on_ready hook must still
	// be pending, otherwise a client could stop polling and lose its output.
	snapshot = waitForHookSnapshot(t, runner, func(s *types.HookOutputSnapshot) bool {
		return s.StartupComplete
	})
	if snapshot.Pending != 1 {
		t.Fatalf("startup completed without pending ready hook: %+v", snapshot)
	}
	if containsHookLine(snapshot, "ready-web") {
		t.Fatal("hook output appeared before its command finished")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot = waitForHookSnapshot(t, runner, func(s *types.HookOutputSnapshot) bool {
		return s.StartupComplete && s.Pending == 0 && containsHookLine(s, "ready-web")
	})
	if !containsHookLine(snapshot, "starting-web") {
		t.Fatal("start hook output missing")
	}
	for _, line := range snapshot.Lines {
		if line.Message == "READY" {
			t.Fatal("service output leaked into the hook journal")
		}
		if line.Process != "web" {
			t.Fatalf("unexpected process scope: %+v", line)
		}
	}
	if snapshot.Lines[0].Message != "starting-web" || snapshot.Lines[1].Message != "ready-web" {
		t.Fatalf("hook order violated: %+v", snapshot.Lines)
	}
}

func TestProjectOnReadyDelaysStartupCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture")
	}
	dir := t.TempDir()
	project := &types.Project{
		Name: "hook-output", ShellConfig: command.DefaultShellConfig(),
		Environment: types.Environment{"RELEASE=" + filepath.Join(dir, "release")},
		OnReady:     &types.HookConfig{Command: `while [ ! -f "$RELEASE" ]; do sleep 0.01; done; printf project-ready`},
		Processes: types.Processes{
			"task": {Name: "task", ReplicaName: "task", Executable: "sh", Args: []string{"-c", "exit 0"}},
		},
	}
	runner, err := NewProjectRunner(&ProjectOpts{project: project, isTuiOn: true, noWatch: true})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- runner.Run() }()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
		_ = runner.ShutDownProject()
	})

	// The project hook is admitted before the readiness signal is published,
	// so a poller sees it as pending while startup is still incomplete.
	waitForHookSnapshot(t, runner, func(s *types.HookOutputSnapshot) bool {
		return s.Pending == 1 && !s.StartupComplete
	})
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := waitForHookSnapshot(t, runner, func(s *types.HookOutputSnapshot) bool {
		return s.StartupComplete && s.Pending == 0 && containsHookLine(s, "project-ready")
	})
	line := snapshot.Lines[0]
	if line.Process != "" || line.Event != "ready" {
		t.Fatalf("project hook scope wrong: %+v", line)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project did not finish after readiness hook")
	}
}
