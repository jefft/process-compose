package app

import (
	"sync"

	"github.com/f1bonacc1/process-compose/src/types"
)

const (
	hookOutputMaxBytes = 256 * 1024
	hookOutputMaxLines = 1024
)

// hookOutputJournal's zero value is usable before Run. Only hook callbacks
// append here; the ordinary service log path deliberately has no journal.
type hookOutputJournal struct {
	mu              sync.Mutex
	lines           [hookOutputMaxLines]types.HookOutputLine
	head            int
	count           int
	bytes           int
	sequence        uint64
	pending         int
	startupComplete bool
}

func (j *hookOutputJournal) reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	clear(j.lines[:])
	j.head, j.count, j.bytes = 0, 0, 0
	j.sequence, j.pending, j.startupComplete = 0, 0, false
}

func hookOutputBytes(line types.HookOutputLine) int {
	return len(line.Process) + len(line.Event) + len(line.Message)
}

func (j *hookOutputJournal) append(process, event, message string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sequence++
	line := types.HookOutputLine{Sequence: j.sequence, Process: process, Event: event, Message: message}
	size := hookOutputBytes(line)
	for j.count > 0 && (j.count == hookOutputMaxLines || j.bytes+size > hookOutputMaxBytes) {
		j.bytes -= hookOutputBytes(j.lines[j.head])
		j.lines[j.head] = types.HookOutputLine{}
		j.head = (j.head + 1) % hookOutputMaxLines
		j.count--
	}
	if size > hookOutputMaxBytes {
		return
	}
	j.lines[(j.head+j.count)%hookOutputMaxLines] = line
	j.count++
	j.bytes += size
}

// Admission is registered before the corresponding lifecycle state is
// published, and remains pending until all output from the command is recorded.
func (j *hookOutputJournal) admit() {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.pending++
	j.mu.Unlock()
}

func (j *hookOutputJournal) complete() {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.pending--
	j.mu.Unlock()
}

func (j *hookOutputJournal) startupReady() {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.startupComplete = true
	j.mu.Unlock()
}

func (j *hookOutputJournal) snapshot(after uint64) *types.HookOutputSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	snapshot := &types.HookOutputSnapshot{
		Lines:        make([]types.HookOutputLine, 0, j.count),
		NextSequence: j.sequence, Pending: j.pending, StartupComplete: j.startupComplete,
	}
	oldest := j.sequence + 1
	if j.count > 0 {
		oldest = j.lines[j.head].Sequence
	}
	snapshot.Truncated = oldest > 0 && after < oldest-1
	for index := range j.count {
		line := j.lines[(j.head+index)%hookOutputMaxLines]
		if line.Sequence > after {
			snapshot.Lines = append(snapshot.Lines, line)
		}
	}
	return snapshot
}

func (p *ProjectRunner) GetHookOutput(after uint64) (*types.HookOutputSnapshot, error) {
	return p.hookOutput.snapshot(after), nil
}
