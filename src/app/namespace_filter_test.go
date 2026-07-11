package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1bonacc1/process-compose/src/admitter"
	"github.com/f1bonacc1/process-compose/src/loader"
	"github.com/f1bonacc1/process-compose/src/types"
)

func loadNamespaceFixture(t *testing.T, namespaces ...string) *types.Project {
	t.Helper()
	fixture := filepath.Join("..", "..", "fixtures-code", "process-compose-namespace-deps.yaml")
	opts := &loader.LoaderOptions{
		FileNames: []string{fixture},
	}
	opts.AddAdmitter(&admitter.NamespaceAdmitter{EnabledNamespaces: namespaces})
	project, err := loader.Load(opts)
	if err != nil {
		t.Fatal(err.Error())
	}
	return project
}

func TestNamespaceFilter_PrunesCrossNamespaceDeps(t *testing.T) {
	project := loadNamespaceFixture(t, "foo")

	if _, ok := project.Processes["bar"]; ok {
		t.Fatal("excluded process bar should be removed from the project")
	}
	foo, ok := project.Processes["foo"]
	if !ok {
		t.Fatal("admitted process foo should stay in the project")
	}
	if len(foo.DependsOn) != 0 {
		t.Fatalf("foo's dependency on the excluded bar should be pruned, got %v", foo.DependsOn)
	}

	runner, err := NewProjectRunner(&ProjectOpts{
		project:         project,
		processesToRun:  []string{},
		mainProcessArgs: []string{},
	})
	if err != nil {
		t.Fatal(err.Error())
	}
	if err := runner.Run(); err != nil {
		t.Fatalf("Run() should succeed with a pruned cross-namespace dependency, got: %v", err)
	}

	state, err := runner.GetProcessState("foo")
	if err != nil {
		t.Fatal(err.Error())
	}
	if state.Status != types.ProcessStateCompleted || state.ExitCode != 0 {
		t.Errorf("foo should complete successfully, got status=%s exit=%d", state.Status, state.ExitCode)
	}

	states, err := runner.GetProcessesState()
	if err != nil {
		t.Fatal(err.Error())
	}
	if len(states.States) != 2 {
		t.Errorf("expected 2 visible processes, got %d", len(states.States))
	}
	for _, s := range states.States {
		if s.Name == "bar" {
			t.Error("excluded process bar should not appear in the states list")
		}
	}
}

func TestNamespaceFilter_IntraNamespaceDepsAreKept(t *testing.T) {
	// no admitted namespaces means admit all - nothing should be pruned
	project := loadNamespaceFixture(t)

	foo := project.Processes["foo"]
	if _, ok := foo.DependsOn["bar"]; !ok {
		t.Fatal("without namespace filtering foo's dependency on bar should be kept")
	}
}

func TestNamespaceFilter_ExplicitSelectionOfExcludedProcessFails(t *testing.T) {
	project := loadNamespaceFixture(t, "foo")

	_, err := NewProjectRunner(&ProjectOpts{
		project:         project,
		processesToRun:  []string{"bar"},
		mainProcessArgs: []string{},
	})
	if err == nil {
		t.Fatal("selecting an excluded process should fail")
	}
	if !strings.Contains(err.Error(), "no such process: bar") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNamespaceFilter_StartExcludedProcessFails(t *testing.T) {
	project := loadNamespaceFixture(t, "foo")

	runner, err := NewProjectRunner(&ProjectOpts{
		project:         project,
		processesToRun:  []string{},
		mainProcessArgs: []string{},
	})
	if err != nil {
		t.Fatal(err.Error())
	}
	if err := runner.StartProcess("bar"); err == nil {
		t.Fatal("starting an excluded process should fail")
	}
}

func TestNamespaceFilter_SurvivesProjectUpdate(t *testing.T) {
	nsAdmitter := &admitter.NamespaceAdmitter{EnabledNamespaces: []string{"foo"}}
	project := loadNamespaceFixture(t, "foo")

	runner, err := NewProjectRunner(&ProjectOpts{
		project:         project,
		processesToRun:  []string{},
		mainProcessArgs: []string{},
		admitters:       []admitter.Admitter{nsAdmitter},
	})
	if err != nil {
		t.Fatal(err.Error())
	}

	// Simulate a config reload: the freshly loaded project has no admission
	// applied (ReloadProject loads without admitters).
	reloaded := loadNamespaceFixture(t)
	if _, err := runner.UpdateProject(reloaded); err != nil {
		t.Fatal(err.Error())
	}

	if _, ok := runner.project.Processes["bar"]; ok {
		t.Fatal("excluded process bar should not be resurrected by a project update")
	}
	foo := runner.project.Processes["foo"]
	if len(foo.DependsOn) != 0 {
		t.Fatalf("foo's pruned dependency should stay pruned after update, got %v", foo.DependsOn)
	}
}
