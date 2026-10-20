package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/f1bonacc1/netstat/netstat"
	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/f1bonacc1/process-compose/src/pclog"
	"github.com/f1bonacc1/process-compose/src/types"
)

func TestExecuteHookEnvironmentShellDirectoryAndOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	metadata := map[string]any{
		"event": "ready", "event_time": time.Now().UTC().Format(time.RFC3339Nano),
		"project_name": "demo", "project_dir": dir,
		"process": map[string]any{"name": "worker-2", "namespace": "jobs", "pid": 321, "restart_count": 3},
		"ports":   map[string]any{"tcp": []uint16{80, 443}, "udp": []uint16{53}, "status": "ok"},
	}
	var lines []string
	executeHook(context.Background(), &types.HookConfig{Command: `printf '%s\n' "$PC_EVENT|$PC_PROJECT_NAME|$PC_PROCESS_NAME|$PC_PROCESS_NAMESPACE|$PC_PROCESS_PID|$PC_RESTART_COUNT|$PC_TCP_PORTS|$PC_UDP_PORTS|$PC_PORTS_STATUS|$SERVICE_VALUE|$PWD"; printf '%s\n' "${PC_EXIT_SIGNAL-unset}|${PC_EXIT_CODE-unset}|${PC_PREVIOUS_TCP_PORTS-unset}"; printf '%s\n' "$PC_EVENT_JSON"; printf 'stderr-line\n' >&2`}, command.ShellConfig{ShellCommand: "sh", ShellArgument: "-c"}, append(os.Environ(), "SERVICE_VALUE=service", "PC_EVENT=stale", "PC_EXIT_SIGNAL=TERM", "PC_EXIT_CODE=99", "PC_PREVIOUS_TCP_PORTS=1"), dir, metadata, func(line string) { lines = append(lines, line) })
	want := "ready|demo|worker-2|jobs|321|3|80 443|53|ok|service|" + dir
	if !slices.Contains(lines, want) || !slices.Contains(lines, "unset|unset|unset") || !slices.Contains(lines, "stderr-line") {
		t.Fatalf("hook output = %q", lines)
	}
	var decoded map[string]any
	for _, line := range lines {
		if strings.HasPrefix(line, "{") {
			if err := json.Unmarshal([]byte(line), &decoded); err != nil {
				t.Fatal(err)
			}
		}
	}
	if decoded["event"] != "ready" || decoded["process"] == nil || decoded["environment"] != nil {
		t.Fatalf("event JSON = %#v", decoded)
	}
}

func TestHookTimeoutAndFailureOnlyLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	for _, test := range []struct {
		name, command string
		timeout       time.Duration
		want          string
	}{
		{"failure", "printf before-failure; exit 9", time.Second, "exit status 9"},
		{"timeout", "sleep 30 & wait", 100 * time.Millisecond, "context deadline exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
			defer cancel()
			var lines []string
			start := time.Now()
			executeHook(ctx, &types.HookConfig{Command: test.command}, *command.DefaultShellConfig(), os.Environ(), t.TempDir(), map[string]any{"event": "start"}, func(line string) { lines = append(lines, line) })
			if time.Since(start) > 3*time.Second || !strings.Contains(strings.Join(lines, "\n"), test.want) {
				t.Fatalf("elapsed=%v output=%q", time.Since(start), lines)
			}
		})
	}
}

func TestHookReadinessBeforeStartAndGenerationIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	var mu sync.Mutex
	var lines []string
	conf := &types.ProcessConfig{Name: "worker", ReplicaName: "worker-2", OnStart: &types.HookConfig{Command: `printf '%s:%s:%s\n' "$PC_EVENT" "$PC_PROCESS_PID" "$PC_RESTART_COUNT"`}, OnReady: &types.HookConfig{Command: `printf '%s:%s:%s\n' "$PC_EVENT" "$PC_PROCESS_PID" "$PC_RESTART_COUNT"`}, OnExit: &types.HookConfig{Command: `printf '%s:%s:%s:%s:%s:%s\n' "$PC_EVENT" "$PC_PROCESS_PID" "$PC_RESTART_COUNT" "$PC_EXIT_CODE" "$PC_STOP_REASON" "${PC_EXIT_SIGNAL-unset}"`}}
	p := NewProcess(withProcConf(conf), withProcState(types.NewProcessState(conf)), withShellConfig(*command.DefaultShellConfig()))
	first := p.newHookInstance(nil)
	capture := func(line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() }
	first.output = capture
	first.markReady()
	first.markReady()
	first.start(123)
	first.markReady()
	first.finish(7, "restart", 0)
	p.procState.Restarts = 1
	second := p.newHookInstance(first)
	second.output = capture
	p.hookInstance = second
	called := false
	p.withInstanceCallback(first, func() { called = true })
	if called {
		t.Fatal("stale generation callback ran")
	}
	second.start(456)
	second.markReady()
	second.finish(0, "none", 0)
	first.recordPorts(hookPorts{tcp: []uint16{999}, status: "ok"})
	select {
	case <-second.done:
	case <-time.After(5 * time.Second):
		t.Fatal("hook dispatch did not finish")
	}
	want := []string{"start:123:0", "ready:123:0", "exit:123:0:7:restart:unset", "start:456:1", "ready:456:1", "exit:456:1:0:none:unset"}
	if !slices.Equal(lines, want) {
		t.Fatalf("events = %q, want %q", lines, want)
	}
}

func TestPortAttributionSortingDeduplicationAndChanges(t *testing.T) {
	entry := func(pid int, port uint16) netstat.SockTabEntry {
		return netstat.SockTabEntry{Process: &netstat.Process{Pid: pid}, LocalAddr: &netstat.SockAddr{Port: port}}
	}
	tables := hookSocketTables{tcp: []netstat.SockTabEntry{entry(42, 8080), entry(42, 80), entry(42, 8080), entry(99, 1)}, udp: []netstat.SockTabEntry{entry(42, 53), entry(42, 53)}}
	ports := tables.portsFor(42, nil)
	if !slices.Equal(ports.tcp, []uint16{80, 8080}) || !slices.Equal(ports.udp, []uint16{53}) {
		t.Fatalf("ports = %#v", ports)
	}
	i := &processHookInstance{started: true, wake: make(chan struct{}, 1), onPortsChanged: &types.HookConfig{Command: "unused"}}
	i.recordPorts(hookPorts{status: "ok"})
	if len(i.queue) != 0 {
		t.Fatal("empty initial snapshot emitted an event")
	}
	i.recordPorts(ports)
	i.recordPorts(ports)
	i.recordPorts(hookPorts{tcp: []uint16{80}, status: "ok"})
	if len(i.queue) != 1 {
		t.Fatalf("pending change count = %d, want 1", len(i.queue))
	}
	previous := i.queue[0].metadata["previous_ports"].(map[string]any)
	current := i.queue[0].metadata["ports"].(map[string]any)
	if len(previous["tcp"].([]uint16)) != 0 || !slices.Equal(current["tcp"].([]uint16), []uint16{80}) {
		t.Fatalf("coalesced transition = %#v -> %#v", previous, current)
	}
	// Once that notification is retained by the consumer, the next transition
	// starts from its snapshot rather than an omitted intermediate poll.
	i.queue = nil
	i.recordPorts(hookPorts{status: "ok"})
	previous = i.queue[0].metadata["previous_ports"].(map[string]any)
	if !slices.Equal(previous["tcp"].([]uint16), []uint16{80}) {
		t.Fatalf("previous snapshot = %#v", previous)
	}
	tables.err = errors.New("discovery unavailable")
	if tables.portsFor(42, nil).status != "error" {
		t.Fatal("discovery error not reported")
	}
}

func TestServiceHooksAcrossAutomaticRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	conf := &types.ProcessConfig{
		Name: "worker", ReplicaName: "worker", Executable: "sh", Args: []string{"-c", "printf 'READY\\n'; exit 7"}, ReadyLogLine: "READY", WorkingDir: dir,
		RestartPolicy: types.RestartPolicyConfig{Restart: types.RestartPolicyOnFailure, MaxRestarts: 1},
		OnStart:       &types.HookConfig{Command: `printf '%s:%s\n' "$PC_EVENT" "$PC_RESTART_COUNT" >> events; exit 9`},
		OnReady:       &types.HookConfig{Command: `printf '%s:%s\n' "$PC_EVENT" "$PC_RESTART_COUNT" >> events`},
		OnExit:        &types.HookConfig{Command: `printf '%s:%s:%s:%s\n' "$PC_EVENT" "$PC_RESTART_COUNT" "$PC_EXIT_CODE" "$PC_STOP_REASON" >> events`},
	}
	p := NewProcess(withProcConf(conf), withProcState(types.NewProcessState(conf)), withShellConfig(*command.DefaultShellConfig()), withLogger(pclog.NewNilLogger()), withProcLog(pclog.NewLogBuffer(100)), withHookProject("demo", dir))
	if code := p.run(); code != 7 {
		t.Fatalf("service exit = %d, want 7", code)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	want := "start:0\nready:0\nexit:0:7:none\nstart:1\nready:1\nexit:1:7:none\n"
	if string(contents) != want {
		t.Fatalf("events=%q, want %q", contents, want)
	}
	if !strings.Contains(strings.Join(p.logBuffer.GetLogRange(0, 100), "\n"), "Hook start failed") {
		t.Fatal("hook failure missing from service log")
	}
	serviceHookPorts.mu.Lock()
	defer serviceHookPorts.mu.Unlock()
	if len(serviceHookPorts.instances) != 0 || serviceHookPorts.stop != nil {
		t.Fatal("port polling active without an on_ports_changed hook")
	}
}
