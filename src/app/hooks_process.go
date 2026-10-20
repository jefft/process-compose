package app

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/f1bonacc1/process-compose/src/types"
)

type processHookEvent struct {
	hook     *types.HookConfig
	metadata map[string]any
}

// Each spawn owns its metadata, readiness latch, port history and queue. A
// successor waits only in its dispatcher, never in the service lifecycle.
type processHookInstance struct {
	mu              sync.Mutex
	wake            chan struct{}
	done            chan struct{}
	queue           []processHookEvent
	started         bool
	readyRequested  bool
	ready           bool
	exited          bool
	pid             int
	restarts        int
	name            string
	namespace       string
	projectName     string
	projectDir      string
	shell           command.ShellConfig
	env             []string
	dir             string
	output          func(string)
	onStart         *types.HookConfig
	onReady         *types.HookConfig
	onExit          *types.HookConfig
	onPortsChanged  *types.HookConfig
	tree            *ProcessTree
	previous        hookPorts
	portsDiscovered bool
}

func (p *Process) newHookInstance(previous *processHookInstance) *processHookInstance {
	p.stateMtx.Lock()
	restarts := p.procState.Restarts
	p.stateMtx.Unlock()
	i := &processHookInstance{
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		restarts: restarts, name: p.getName(), namespace: p.procConf.Namespace.String(),
		projectName: p.hookProjectName, projectDir: p.hookProjectDir,
		shell: p.shellConfig, dir: p.procConf.WorkingDir,
		output: p.handleInfo, tree: p.processTree,
		onStart: p.procConf.OnStart, onReady: p.procConf.OnReady,
		onExit: p.procConf.OnExit, onPortsChanged: p.procConf.OnPortsChanged,
	}
	if i.onStart != nil || i.onReady != nil || i.onExit != nil || i.onPortsChanged != nil {
		i.env = p.getProcessEnvironment()
		if p.linkHookInstance != nil {
			previous = p.linkHookInstance(i)
		}
		go i.run(previous)
	} else {
		close(i.done)
	}
	return i
}

func (i *processHookInstance) metadata(event string) map[string]any {
	return map[string]any{
		"event": event, "event_time": time.Now().UTC().Format(time.RFC3339Nano),
		"project_name": i.projectName, "project_dir": i.projectDir,
		"process": map[string]any{
			"name": i.name, "namespace": i.namespace, "pid": i.pid, "restart_count": i.restarts,
		},
	}
}

// enqueue is called with i.mu held. It never waits on a command or a consumer.
func (i *processHookInstance) enqueue(hook *types.HookConfig, metadata map[string]any) {
	if hook == nil {
		return
	}
	if metadata["event"] == "ports_changed" {
		for index := range i.queue {
			pending := &i.queue[index]
			if pending.metadata["event"] != "ports_changed" {
				continue
			}
			// Keep the transition's original baseline, not the most recent
			// poll: intermediate observations may never have been delivered.
			metadata["previous_ports"] = pending.metadata["previous_ports"]
			if equalHookPortMetadata(metadata["ports"], metadata["previous_ports"]) {
				copy(i.queue[index:], i.queue[index+1:])
				i.queue[len(i.queue)-1] = processHookEvent{}
				i.queue = i.queue[:len(i.queue)-1]
			} else {
				// The newest observation follows any readiness notification
				// that arrived since the replaced port observation.
				copy(i.queue[index:], i.queue[index+1:])
				i.queue[len(i.queue)-1] = processHookEvent{hook: hook, metadata: metadata}
			}
			return
		}
	}
	i.queue = append(i.queue, processHookEvent{hook: hook, metadata: metadata})
	select {
	case i.wake <- struct{}{}:
	default:
	}
}

func (i *processHookInstance) run(previous *processHookInstance) {
	defer close(i.done)
	if previous != nil {
		<-previous.done
	}
	for {
		i.mu.Lock()
		if len(i.queue) == 0 {
			exited := i.exited
			i.mu.Unlock()
			if exited {
				return
			}
			<-i.wake
			continue
		}
		event := i.queue[0]
		i.queue[0] = processHookEvent{}
		i.queue = i.queue[1:]
		i.mu.Unlock()
		executeHook(context.Background(), event.hook, i.shell, i.env, i.dir, event.metadata, i.output)
	}
}

func (i *processHookInstance) start(pid int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.exited || i.started {
		return
	}
	i.pid = pid
	i.started = true
	if i.onStart != nil {
		i.enqueue(i.onStart, i.metadata("start"))
	}
	if i.readyRequested {
		i.readyLocked()
	}
	if i.onPortsChanged != nil {
		serviceHookPorts.add(i)
	}
}

func (i *processHookInstance) markReady() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.exited || i.ready {
		return
	}
	i.readyRequested = true
	if i.started {
		i.readyLocked()
	}
}

func (i *processHookInstance) readyLocked() {
	i.ready = true
	if i.onReady == nil {
		return
	}
	metadata := i.metadata("ready")
	metadata["ports"] = sampleHookPorts(i.pid, i.tree).metadata()
	i.enqueue(i.onReady, metadata)
}

func (i *processHookInstance) finish(code int, reason string, signal int) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.exited {
		return
	}
	// Remove the subscription before publishing exit. A poll already in flight
	// observes exited below and cannot append an event after exit.
	if i.onPortsChanged != nil {
		serviceHookPorts.remove(i)
	}
	if i.started && i.onExit != nil {
		metadata := i.metadata("exit")
		metadata["exit_code"], metadata["stop_reason"] = code, reason
		if signal != 0 {
			metadata["exit_signal"] = signal
		}
		i.enqueue(i.onExit, metadata)
	}
	i.exited = true
	select {
	case i.wake <- struct{}{}:
	default:
	}
}

func (i *processHookInstance) recordPorts(current hookPorts) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.exited || !i.started {
		return
	}
	if current.status == "error" {
		// A failed discovery is not evidence that previously open ports closed.
		current.tcp, current.udp = i.previous.tcp, i.previous.udp
	}
	if !i.portsDiscovered && len(current.tcp)+len(current.udp) == 0 {
		return
	}
	if i.portsDiscovered && current.equal(i.previous) {
		return
	}
	metadata := i.metadata("ports_changed")
	metadata["ports"] = current.metadata()
	if i.previous.status == "" {
		i.previous.status = "ok"
	}
	metadata["previous_ports"] = i.previous.metadata()
	i.enqueue(i.onPortsChanged, metadata)
	i.previous = current
	i.portsDiscovered = true
}

type hookPorts struct {
	tcp    []uint16
	udp    []uint16
	status string
}

func (p hookPorts) metadata() map[string]any {
	if p.tcp == nil {
		p.tcp = []uint16{}
	}
	if p.udp == nil {
		p.udp = []uint16{}
	}
	if p.status == "" {
		p.status = "ok"
	}
	return map[string]any{"tcp": p.tcp, "udp": p.udp, "status": p.status}
}

func (p hookPorts) equal(other hookPorts) bool {
	return p.status == other.status && slices.Equal(p.tcp, other.tcp) && slices.Equal(p.udp, other.udp)
}

func equalHookPortMetadata(left, right any) bool {
	a, aOK := left.(map[string]any)
	b, bOK := right.(map[string]any)
	if !aOK || !bOK {
		return false
	}
	aTCP, _ := a["tcp"].([]uint16)
	bTCP, _ := b["tcp"].([]uint16)
	aUDP, _ := a["udp"].([]uint16)
	bUDP, _ := b["udp"].([]uint16)
	return a["status"] == b["status"] && slices.Equal(aTCP, bTCP) && slices.Equal(aUDP, bUDP)
}

func (p *Process) hasReadinessProbe() bool {
	p.probesMtx.Lock()
	defer p.probesMtx.Unlock()
	return p.readyProber != nil
}

func (p *Process) withInstanceCallback(instance *processHookInstance, callback func()) {
	p.callbackMtx.RLock()
	defer p.callbackMtx.RUnlock()
	if instance == nil || p.hookInstance != instance {
		return
	}
	instance.mu.Lock()
	exited := instance.exited
	instance.mu.Unlock()
	if !exited {
		callback()
	}
}

func (p *Process) hookStopReason() string {
	if p.isBeingRestarted() {
		return "restart"
	}
	if p.isStopped.Load() || (p.procRunCtx != nil && p.procRunCtx.Err() != nil) {
		return "shutdown"
	}
	return "none"
}

func (p *Process) waitForHooks() {
	p.callbackMtx.RLock()
	instance := p.hookInstance
	p.callbackMtx.RUnlock()
	if instance == nil {
		return
	}
	instance.mu.Lock()
	exited := instance.exited
	instance.mu.Unlock()
	if exited {
		<-instance.done
	}
}

// The runner owns the name's tail, not the current Process object. Manual,
// watched and configuration-triggered replacements therefore join the same
// notification chain as automatic restarts, without delaying service startup.
func (p *ProjectRunner) linkServiceHookInstance(instance *processHookInstance) *processHookInstance {
	p.hookChainsMutex.Lock()
	defer p.hookChainsMutex.Unlock()
	if p.hookChains == nil {
		p.hookChains = make(map[string]*processHookInstance)
	}
	previous := p.hookChains[instance.name]
	p.hookChains[instance.name] = instance
	return previous
}

func (p *ProjectRunner) waitForServiceHooks() {
	for {
		p.hookChainsMutex.Lock()
		tails := make(map[string]*processHookInstance, len(p.hookChains))
		for name, instance := range p.hookChains {
			tails[name] = instance
		}
		p.hookChainsMutex.Unlock()
		for _, instance := range tails {
			<-instance.done
		}
		p.hookChainsMutex.Lock()
		unchanged := len(tails) == len(p.hookChains)
		for name, instance := range p.hookChains {
			if tails[name] != instance {
				unchanged = false
				break
			}
		}
		p.hookChainsMutex.Unlock()
		if unchanged {
			return
		}
	}
}
