package app

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/f1bonacc1/netstat/netstat"
)

// The monitor is shared by services (and concurrent runners): each tick scans
// each socket table once, then attributes that snapshot to all subscribers.
// No goroutine or ticker remains when the last subscription is removed.
var serviceHookPorts = hookPortMonitor{instances: make(map[*processHookInstance]struct{})}

type hookPortMonitor struct {
	mu        sync.Mutex
	instances map[*processHookInstance]struct{}
	stop      chan struct{}
}

func (m *hookPortMonitor) add(instance *processHookInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.instances[instance] = struct{}{}
	if m.stop != nil {
		return
	}
	stop := make(chan struct{})
	m.stop = stop
	go m.run(stop)
}

func (m *hookPortMonitor) remove(instance *processHookInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.instances, instance)
	if len(m.instances) == 0 && m.stop != nil {
		close(m.stop)
		m.stop = nil
	}
}

func (m *hookPortMonitor) run(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.mu.Lock()
			instances := make([]*processHookInstance, 0, len(m.instances))
			for instance := range m.instances {
				instances = append(instances, instance)
			}
			m.mu.Unlock()
			if len(instances) == 0 {
				continue
			}
			tables := readHookSocketTables()
			for _, instance := range instances {
				select {
				case <-stop:
					return
				default:
				}
				instance.recordPorts(tables.portsFor(instance.pid, instance.tree))
			}
		}
	}
}

type hookSocketTables struct {
	tcp []netstat.SockTabEntry
	udp []netstat.SockTabEntry
	err error
}

func readHookSocketTables() hookSocketTables {
	tcpFilter := func(s *netstat.SockTabEntry) bool { return s.State == netstat.Listen }
	udpFilter := func(s *netstat.SockTabEntry) bool { return s.RemoteAddr != nil && s.RemoteAddr.Port == 0 }
	tcp, tcpErr := netstat.TCPSocks(tcpFilter)
	tcp6, tcp6Err := netstat.TCP6Socks(tcpFilter)
	udp, udpErr := netstat.UDPSocks(udpFilter)
	udp6, udp6Err := netstat.UDP6Socks(udpFilter)
	return hookSocketTables{
		tcp: append(tcp, tcp6...), udp: append(udp, udp6...),
		err: errors.Join(tcpErr, tcp6Err, udpErr, udp6Err),
	}
}

func sampleHookPorts(pid int, tree *ProcessTree) hookPorts {
	return readHookSocketTables().portsFor(pid, tree)
}

func (tables hookSocketTables) portsFor(pid int, tree *ProcessTree) hookPorts {
	ports := hookPorts{status: "ok"}
	if tables.err != nil {
		ports.status = "error"
	}
	pids := map[int]struct{}{pid: {}}
	if tree != nil {
		if err := tree.Update(); err != nil {
			ports.status = "error"
		} else {
			for _, child := range tree.GetDescendants(int32(pid)) {
				pids[int(child.Pid)] = struct{}{}
			}
		}
	}
	collect := func(entries []netstat.SockTabEntry) []uint16 {
		var result []uint16
		for _, entry := range entries {
			if entry.Process == nil || entry.LocalAddr == nil {
				continue
			}
			if _, ok := pids[entry.Process.Pid]; ok {
				result = append(result, entry.LocalAddr.Port)
			}
		}
		slices.Sort(result)
		return slices.Compact(result)
	}
	ports.tcp, ports.udp = collect(tables.tcp), collect(tables.udp)
	return ports
}
