package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/f1bonacc1/process-compose/src/loader"
	"github.com/f1bonacc1/process-compose/src/types"
)

const updateAPIProjectYaml = `
processes:
  keeper:
    command: "sleep 600"
    vars:
      PORT: 8080
    availability:
      restart: always
  oneshot:
    command: "echo done"
    availability:
      restart: "no"
`

// loadAsAPIPayload loads a project the way `process-compose project update`
// does and round-trips it through JSON, as the REST client and the POST
// /project handler do. JSON decodes every number in the free-form vars as
// float64 while the in-process loader yields int.
func loadAsAPIPayload(t *testing.T, yaml string) *types.Project {
	t.Helper()
	file := filepath.Join(t.TempDir(), "process-compose.yaml")
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := loader.Load(&loader.LoaderOptions{FileNames: []string{file}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var decoded types.Project
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	return &decoded
}

func startUpdateAPIProject(t *testing.T) *ProjectRunner {
	t.Helper()
	file := filepath.Join(t.TempDir(), "process-compose.yaml")
	if err := os.WriteFile(file, []byte(updateAPIProjectYaml), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := loader.Load(&loader.LoaderOptions{FileNames: []string{file}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewProjectRunner(&ProjectOpts{
		project:         project,
		processesToRun:  []string{},
		mainProcessArgs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run()
	}()
	t.Cleanup(func() {
		_ = runner.ShutDownProject()
		if err := <-runErr; err != nil {
			t.Logf("runner returned an error on shutdown: %v", err)
		}
	})
	waitForProcessLaunched(t, runner, "keeper", 10*time.Second)
	waitForProcessState(t, runner, "keeper", types.ProcessStateRunning, 10*time.Second)
	waitForProcessState(t, runner, "oneshot", types.ProcessStateCompleted, 10*time.Second)
	return runner
}

func processPid(t *testing.T, runner *ProjectRunner, name string) int {
	t.Helper()
	state, err := runner.GetProcessState(name)
	if err != nil {
		t.Fatal(err)
	}
	return state.Pid
}

// TestUpdateProject_IdenticalAPIPayloadKeepsProcesses is a regression test for
// `project update` with an unchanged file restarting every process: the
// JSON-decoded vars (float64) never DeepEqual'd the loaded ones (int).
func TestUpdateProject_IdenticalAPIPayloadKeepsProcesses(t *testing.T) {
	runner := startUpdateAPIProject(t)
	keeperPid := processPid(t, runner, "keeper")
	oneshotPid := processPid(t, runner, "oneshot")

	status, err := runner.UpdateProject(loadAsAPIPayload(t, updateAPIProjectYaml))
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 0 {
		t.Fatalf("an identical project must not update anything, got %v", status)
	}

	// Give a wrongly restarted process time to show up with a new PID.
	time.Sleep(500 * time.Millisecond)
	if pid := processPid(t, runner, "keeper"); pid != keeperPid {
		t.Errorf("keeper was restarted: pid %d -> %d", keeperPid, pid)
	}
	state, err := runner.GetProcessState("oneshot")
	if err != nil {
		t.Fatal(err)
	}
	if state.Pid != oneshotPid || state.Status != types.ProcessStateCompleted {
		t.Errorf("oneshot was re-run: pid %d -> %d, status %s", oneshotPid, state.Pid, state.Status)
	}
}

func TestUpdateProject_ChangedAPIPayloadUpdatesOnlyChangedProcess(t *testing.T) {
	runner := startUpdateAPIProject(t)
	keeperPid := processPid(t, runner, "keeper")
	oneshotPid := processPid(t, runner, "oneshot")

	changed := loadAsAPIPayload(t, updateAPIProjectYaml)
	keeper := changed.Processes["keeper"]
	keeper.Vars["PORT"] = 9090
	changed.Processes["keeper"] = keeper

	status, err := runner.UpdateProject(changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 1 || status["keeper"] != types.ProcessUpdateUpdated {
		t.Fatalf("want only keeper updated, got %v", status)
	}
	waitForProcessState(t, runner, "keeper", types.ProcessStateRunning, 10*time.Second)
	if pid := processPid(t, runner, "keeper"); pid == keeperPid {
		t.Errorf("keeper should have been restarted with its changed vars, pid still %d", pid)
	}
	if pid := processPid(t, runner, "oneshot"); pid != oneshotPid {
		t.Errorf("unchanged oneshot was re-run: pid %d -> %d", oneshotPid, pid)
	}
}
