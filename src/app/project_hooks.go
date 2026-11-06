package app

import (
	"context"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/f1bonacc1/process-compose/src/types"
	"github.com/rs/zerolog/log"
)

// projectReadyHook only copies notifications under the broadcaster lock. The
// Run loop evaluates readiness, samples ports, and executes the command after
// all process lifecycle locks have been released.
type projectReadyHook struct {
	mutex     sync.Mutex
	wake      chan struct{}
	processes map[string]types.ProcessConfig
	states    map[string]types.ProcessState
	fired     bool
	runner    *ProjectRunner
	hook      *types.HookConfig
	shell     command.ShellConfig
	env       []string
	name      string
	dir       string
}

func newProjectReadyHook(p *ProjectRunner, runOrder []types.ProcessConfig, dir string) *projectReadyHook {
	// The observer stays registered without a root hook: it publishes the
	// project readiness signal the startup output API waits on. Environment
	// and shell are only needed to execute a configured command.
	h := &projectReadyHook{
		wake:      make(chan struct{}, 1),
		processes: make(map[string]types.ProcessConfig),
		states:    make(map[string]types.ProcessState),
		runner:    p, hook: p.project.OnReady.Clone(),
		name: p.project.Name, dir: dir,
	}
	if h.hook != nil {
		shell := p.project.ShellConfig
		if shell == nil {
			shell = command.DefaultShellConfig()
		}
		h.shell = *shell
		for key, value := range p.project.DotEnvVars {
			h.env = append(h.env, key+"="+value)
		}
		h.env = append(h.env, os.Environ()...)
		h.env = append(h.env, p.project.Environment...)
	}
	for _, proc := range runOrder {
		if proc.IsDeferred() || (proc.Schedule != nil && proc.Schedule.IsScheduled()) {
			continue
		}
		h.processes[proc.ReplicaName] = proc
	}
	return h
}

func (h *projectReadyHook) UniqueID() string { return "project-on-ready" }

func (h *projectReadyHook) Notify(event types.ProcessStateEvent) {
	if event.Snapshot {
		return
	}
	h.mutex.Lock()
	if _, selected := h.processes[event.State.Name]; !selected || h.fired {
		h.mutex.Unlock()
		return
	}
	h.states[event.State.Name] = event.State
	h.mutex.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *projectReadyHook) notifications() <-chan struct{} {
	if h == nil {
		return nil
	}
	return h.wake
}

func projectProcessReady(proc types.ProcessConfig, state types.ProcessState) bool {
	if state.Status == types.ProcessStateCompleted || state.Status == types.ProcessStateWatching {
		return proc.IsExitCodeSuccess(state.ExitCode)
	}
	if state.Status != types.ProcessStateRunning && state.Status != types.ProcessStateLaunched {
		return false
	}
	if proc.ReadinessProbe != nil || proc.ReadyLogLine != "" {
		return state.ProcessReadyTime != nil
	}
	return state.IsRunning
}

func (h *projectReadyHook) readySnapshot() ([]types.ProcessState, bool) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if h.fired {
		return nil, false
	}
	for name, proc := range h.processes {
		state, found := h.states[name]
		if !found || !projectProcessReady(proc, state) {
			return nil, false
		}
	}
	states := make([]types.ProcessState, 0, len(h.processes))
	for _, state := range h.states {
		states = append(states, state)
	}
	h.fired = true
	return states, true
}

func (h *projectReadyHook) runIfReady() {
	if h == nil {
		return
	}
	states, ready := h.readySnapshot()
	if !ready {
		return
	}
	if len(states) == 0 || h.hook == nil {
		h.runner.hookOutput.startupReady()
		return
	}
	// Deferred calls run in reverse order: the project readiness signal is
	// published only after the hook's output is fully journaled and its
	// pending admission is released.
	h.runner.hookOutput.admit()
	defer h.runner.hookOutput.startupReady()
	defer h.runner.hookOutput.complete()
	slices.SortFunc(states, func(a, b types.ProcessState) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	processes := make([]map[string]any, 0, len(states))
	for _, state := range states {
		process := map[string]any{
			"name": state.Name, "namespace": state.Namespace.String(),
			"pid": state.Pid, "restart_count": state.Restarts,
		}
		if ports, err := h.runner.GetProcessPorts(state.Name); err == nil {
			slices.Sort(ports.TcpPorts)
			slices.Sort(ports.UdpPorts)
			ports.TcpPorts = slices.Compact(ports.TcpPorts)
			ports.UdpPorts = slices.Compact(ports.UdpPorts)
			process["ports"] = map[string]any{"tcp": ports.TcpPorts, "udp": ports.UdpPorts, "status": "ok"}
		}
		processes = append(processes, process)
	}
	metadata := map[string]any{
		"event": "ready", "event_time": time.Now().UTC().Format(time.RFC3339Nano),
		"project_name": h.name, "project_dir": h.dir, "processes": processes,
	}
	// Successful short-lived tasks can finish the project before this loop
	// consumes their state event. Their notification still gets its bounded
	// execution window, independent of the application's cancellation.
	executeHook(context.WithoutCancel(h.runner.ctxApp), h.hook, h.shell, h.env, h.dir, metadata, func(message string) {
		h.runner.hookOutput.append("", "ready", message)
		h.runner.logger.Info(message, "project", 0)
		log.Info().Msg(message)
	})
}

func hookProjectDirectory() string {
	dir, err := os.Getwd()
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve hook project directory")
	}
	return dir
}
