package admitter

import (
	"github.com/f1bonacc1/process-compose/src/types"
	"github.com/rs/zerolog/log"
)

type Admitter interface {
	Admit(config *types.ProcessConfig) bool
}

// ApplyToProject removes every process rejected by one of the admitters and
// prunes the remaining processes' dependencies on the removed ones, so that
// a process selected e.g. by --namespace can start without waiting for an
// excluded dependency. Dependencies on undefined processes are validated
// before admission, so the pruning only ever drops edges that point at
// excluded processes.
func ApplyToProject(p *types.Project, admitters []Admitter) {
	removed := false
	for name, proc := range p.Processes {
		for _, adm := range admitters {
			if !adm.Admit(&proc) {
				log.Info().Msgf("Process %s was removed due to admission policy", proc.ReplicaName)
				delete(p.Processes, name)
				removed = true
				break
			}
		}
	}
	if !removed {
		return
	}
	for name, proc := range p.Processes {
		for dep := range proc.DependsOn {
			if !processExists(p, dep) {
				log.Warn().Msgf("Process %s depends on %s, which was removed due to admission policy - removing the dependency", name, dep)
				delete(proc.DependsOn, dep)
			}
		}
	}
}

// processExists mirrors the lookup semantics of Project.GetProcesses:
// a dependency can reference a process by replica name (the map key) or by name.
func processExists(p *types.Project, name string) bool {
	if _, ok := p.Processes[name]; ok {
		return true
	}
	for _, proc := range p.Processes {
		if proc.Name == name {
			return true
		}
	}
	return false
}
