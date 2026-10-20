package app

import (
	"context"
	"encoding/json"
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

func TestProjectReadyHookOncePerRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "events")
	project := &types.Project{
		Name: "hook-test", ShellConfig: command.DefaultShellConfig(),
		Environment: types.Environment{"HOOK_OUTPUT=" + file, "HOOK_VALUE=project"},
		OnReady:     &types.HookConfig{Command: `printf '%s\n' "$PC_EVENT_JSON" >> "$HOOK_OUTPUT"; printf '%s:%s\n' "$HOOK_VALUE" "$PWD" >> "$HOOK_OUTPUT"`},
	}
	runner := &ProjectRunner{project: project, ctxApp: context.Background(), logger: pclog.NewNilLogger()}
	services := []types.ProcessConfig{
		{Name: "db", ReplicaName: "db"},
		{Name: "web", ReplicaName: "web", ReadyLogLine: "READY"},
		{Name: "deferred", ReplicaName: "deferred", Disabled: true},
		{Name: "foreground", ReplicaName: "foreground", IsForeground: true},
		{Name: "scheduled", ReplicaName: "scheduled", Schedule: &types.ScheduleConfig{Interval: "1h"}},
	}
	for range 2 {
		hook := newProjectReadyHook(runner, services, dir)
		hook.Notify(types.ProcessStateEvent{Snapshot: true, State: types.ProcessState{Name: "web", Status: types.ProcessStateRunning, IsRunning: true}})
		hook.Notify(types.ProcessStateEvent{State: types.ProcessState{Name: "db", Status: types.ProcessStateCompleted, ExitCode: 0}})
		if _, ready := hook.readySnapshot(); ready {
			t.Fatal("project ready before web startup")
		}
		hook.Notify(types.ProcessStateEvent{State: types.ProcessState{Name: "web", Status: types.ProcessStateRunning, IsRunning: true}})
		if _, ready := hook.readySnapshot(); ready {
			t.Fatal("project ready before readiness log")
		}
		now := time.Now()
		hook.Notify(types.ProcessStateEvent{State: types.ProcessState{Name: "web", Status: types.ProcessStateRunning, IsRunning: true, ProcessReadyTime: &now}})
		hook.runIfReady()
		hook.runIfReady()
		hook.Notify(types.ProcessStateEvent{State: types.ProcessState{Name: "web", Status: types.ProcessStateRunning, IsRunning: true, ProcessReadyTime: &now}})
		hook.runIfReady()
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected exactly one hook per Run, got %q", data)
	}
	for _, index := range []int{0, 2} {
		var event struct {
			Event       string `json:"event"`
			ProjectName string `json:"project_name"`
			Processes   []struct {
				Name string `json:"name"`
			} `json:"processes"`
		}
		if err := json.Unmarshal([]byte(lines[index]), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event != "ready" || event.ProjectName != "hook-test" || len(event.Processes) != 2 || event.Processes[0].Name != "db" || event.Processes[1].Name != "web" {
			t.Fatalf("unexpected project event: %+v", event)
		}
		if lines[index+1] != "project:"+dir {
			t.Fatalf("project environment/cwd = %q", lines[index+1])
		}
	}
}

func TestProjectReadyHookFailureDoesNotFire(t *testing.T) {
	runner := &ProjectRunner{project: &types.Project{ShellConfig: command.DefaultShellConfig(), OnReady: &types.HookConfig{Command: "echo ready"}}}
	hook := newProjectReadyHook(runner, []types.ProcessConfig{{Name: "task", ReplicaName: "task", SuccessExitCodes: []int{3}}}, t.TempDir())
	for _, state := range []types.ProcessState{
		{Name: "task", Status: types.ProcessStateError, ExitCode: 1},
		{Name: "task", Status: types.ProcessStateCompleted, ExitCode: 1},
		{Name: "task", Status: types.ProcessStateSkipped},
		{Name: "unselected", Status: types.ProcessStateRunning, IsRunning: true},
	} {
		hook.Notify(types.ProcessStateEvent{State: state})
		if _, ready := hook.readySnapshot(); ready {
			t.Fatalf("project fired for state %+v", state)
		}
	}
	hook.Notify(types.ProcessStateEvent{State: types.ProcessState{Name: "task", Status: types.ProcessStateCompleted, ExitCode: 3}})
	if _, ready := hook.readySnapshot(); !ready {
		t.Fatal("successful completed task did not satisfy startup")
	}
}
