package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1bonacc1/process-compose/src/types"
)

func loadHookFixture(t *testing.T, contents string) (*types.Project, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(&LoaderOptions{FileNames: []string{file}, IsInternalLoader: true, disableDotenv: true})
}

func TestLoadLifecycleHooks(t *testing.T) {
	t.Setenv("PC_EVENT", "wrong-load-time-value")
	t.Setenv("PC_PROCESS_NAME", "wrong-load-time-name")
	t.Setenv("HOOK_ORDINARY_VARIABLE", "expanded")
	project, err := loadHookFixture(t, `is_strict: true
vars:
  greeting: hello
on_ready:
  command: 'echo {{.greeting}} $PC_EVENT ${PC_PROJECT_NAME:-fallback} ${HOOK_ORDINARY_VARIABLE}'
processes:
  base:
    command: 'echo base'
    on_start:
      command: 'echo base $PC_EVENT'
      timeout_seconds: 4
    on_ready:
      command: 'echo ready {{.PC_REPLICA_NUM}} $PC_PROCESS_NAME'
    on_exit:
      command: 'echo $PC_EXIT_CODE'
    on_ports_changed:
      command: 'echo ${PC_TCP_PORTS:-none} $PC_PREVIOUS_TCP_PORTS'
  worker:
    extends: base
    replicas: 2
    on_start:
      command: 'echo child {{.PC_REPLICA_NUM}} $PC_PROCESS_NAME'
`)
	if err != nil {
		t.Fatal(err)
	}
	wantProject := "echo hello $PC_EVENT ${PC_PROJECT_NAME:-fallback} expanded"
	if project.OnReady.Command != wantProject {
		t.Fatalf("project hook = %q, want %q", project.OnReady.Command, wantProject)
	}
	for replica := range 2 {
		proc := project.Processes[fmt.Sprintf("worker-%d", replica)]
		wantStart := fmt.Sprintf("echo child %d $PC_PROCESS_NAME", replica)
		if proc.OnStart.Command != wantStart || proc.OnStart.TimeoutSeconds != 4 {
			t.Errorf("replica %d start = %+v", replica, proc.OnStart)
		}
		wantReady := fmt.Sprintf("echo ready %d $PC_PROCESS_NAME", replica)
		if proc.OnReady.Command != wantReady {
			t.Errorf("replica %d ready = %q, want %q", replica, proc.OnReady.Command, wantReady)
		}
		if proc.OnPortsChanged.Command != "echo ${PC_TCP_PORTS:-none} $PC_PREVIOUS_TCP_PORTS" {
			t.Errorf("ports expression expanded at load time: %q", proc.OnPortsChanged.Command)
		}
	}
	first := project.Processes["worker-0"]
	second := project.Processes["worker-1"]
	first.OnReady.Command = "changed"
	if second.OnReady.Command == "changed" || project.Processes["base"].OnReady.Command == "changed" {
		t.Fatal("replicas or inherited hooks share mutable configuration")
	}
}

func TestLoadRejectsInvalidLifecycleHooks(t *testing.T) {
	for _, target := range []string{"project", "on_start", "on_ready", "on_exit", "on_ports_changed"} {
		for _, block := range []string{"command: '   '", "command: echo ok\ntimeout_seconds: -1"} {
			t.Run(target+"/"+block, func(t *testing.T) {
				config := "is_strict: true\nprocesses:\n  worker:\n    command: echo ok\n"
				if target == "project" {
					config += "on_ready:\n  " + strings.ReplaceAll(block, "\n", "\n  ") + "\n"
				} else {
					config += "    " + target + ":\n      " + strings.ReplaceAll(block, "\n", "\n      ") + "\n"
				}
				if _, err := loadHookFixture(t, config); err == nil {
					t.Fatal("invalid hook was accepted")
				}
			})
		}
	}
}

func TestMergeProjectLifecycleHook(t *testing.T) {
	base := &types.Project{OnReady: &types.HookConfig{Command: "old", TimeoutSeconds: 3}}
	override := &types.Project{OnReady: &types.HookConfig{Command: "new"}}
	if err := mergeProjects(base, override); err != nil {
		t.Fatal(err)
	}
	if base.OnReady.Command != "new" || base.OnReady.TimeoutSeconds != 3 {
		t.Fatalf("merged hook = %+v", base.OnReady)
	}
}

func TestHookRuntimeExpansionScopeAndExpressions(t *testing.T) {
	t.Setenv("PC_EVENT", "load-time-event")
	t.Setenv("PC_PROCESS_NAME", "load-time-name")
	t.Setenv("PC_TCP_PORTS", "9000")
	t.Setenv("OTHER", "x")
	t.Setenv("FALLBACK", "load-time-fallback")
	t.Setenv("HOOK_ORDINARY_VARIABLE", "expanded")
	t.Setenv("HOOK_SENTINEL", "__PC_HOOK_RUNTIME_VARIABLE__0__")
	t.Setenv("HOOK_SENTINEL_TRANSFORMED", "__pc_hook_runtime_variable___0__")
	project, err := loadHookFixture(t, `is_strict: true
x-shared: &shared 'echo ${PC_EVENT}'
environment:
  - EVENT=${PC_EVENT}
on_ready:
  command: 'echo ${OTHER:-$PC_EVENT} $PC_PROCESS_NAME'
processes:
  worker:
    command: *shared
    environment:
      - NAME=${PC_PROCESS_NAME}
    on_start:
      command: *shared
    on_ready:
      command: 'echo ${#PC_TCP_PORTS} ${PC_TCP_PORTS:-${FALLBACK}} ${HOOK_ORDINARY_VARIABLE}'
    on_exit:
      command: 'echo ##PC_HOOK_RUNTIME_VARIABLE## __PC_HOOK_RUNTIME_VARIABLE_0__ ${HOOK_SENTINEL} ${HOOK_SENTINEL_TRANSFORMED^^} ${PC_EVENT:-''quoted''} ${PC_EVENT:-$$FALLBACK}'
`)
	if err != nil {
		t.Fatal(err)
	}
	worker := project.Processes["worker"]
	if worker.Command != "echo load-time-event" {
		t.Errorf("ordinary aliased command expansion changed: %q", worker.Command)
	}
	if project.Environment[0] != "EVENT=load-time-event" || worker.Environment[0] != "NAME=load-time-name" {
		t.Errorf("ordinary environment expansion changed: project=%v process=%v", project.Environment, worker.Environment)
	}
	if worker.OnStart.Command != "echo ${PC_EVENT}" {
		t.Errorf("aliased hook was expanded: %q", worker.OnStart.Command)
	}
	if project.OnReady.Command != "echo ${OTHER:-$PC_EVENT} $PC_PROCESS_NAME" {
		t.Errorf("runtime-dependent default expression changed: %q", project.OnReady.Command)
	}
	wantReady := "echo ${#PC_TCP_PORTS} ${PC_TCP_PORTS:-${FALLBACK}} expanded"
	if worker.OnReady.Command != wantReady {
		t.Errorf("nested/length expression = %q, want %q", worker.OnReady.Command, wantReady)
	}
	wantExit := "echo ##PC_HOOK_RUNTIME_VARIABLE## __PC_HOOK_RUNTIME_VARIABLE_0__ __PC_HOOK_RUNTIME_VARIABLE__0__ __PC_HOOK_RUNTIME_VARIABLE___0__ ${PC_EVENT:-'quoted'} ${PC_EVENT:-$FALLBACK}"
	if worker.OnExit.Command != wantExit {
		t.Errorf("literal marker, injected marker, quoting or escaping changed: %q, want %q", worker.OnExit.Command, wantExit)
	}
}

func TestHookRuntimeVariablesAfterStructuralEnvironmentExpansion(t *testing.T) {
	t.Setenv("CONFIG_ENV", "MODE=dev")
	t.Setenv("PC_EVENT", "")
	t.Setenv("PC_PROCESS_NAME", "expanded")
	t.Setenv("PC_PROJECT_NAME", "true")
	t.Setenv("PC_RESTART_COUNT", "2")
	project, err := loadHookFixture(t, `is_strict: true
environment: [${CONFIG_ENV}]
is_tui_disabled: ${PC_PROJECT_NAME}
x-string: '${PC_PROJECT_NAME}'
x-empty: ${PC_EVENT}
x-shared: &shared 'echo ${PC_PROCESS_NAME}'
on_ready:
  command: 'echo ${PC_EVENT:-''project''}'
processes:
  worker:
    command: *shared
    replicas: ${PC_RESTART_COUNT}
    environment: [NAME=${PC_PROCESS_NAME}]
    on_start:
      command: 'echo ${PC_EVENT}'
    on_ready:
      command: *shared
    on_exit:
      command: 'echo ${PC_EVENT:-''quoted''}'
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Environment) != 1 || project.Environment[0] != "MODE=dev" {
		t.Errorf("structural environment expansion changed: %v", project.Environment)
	}
	if !project.IsTuiDisabled || project.Extensions["x-string"] != "true" || project.Extensions["x-empty"] != nil {
		t.Errorf("scalar typing changed: tui=%v extensions=%v", project.IsTuiDisabled, project.Extensions)
	}
	if project.OnReady.Command != "echo ${PC_EVENT:-'project'}" {
		t.Errorf("project hook quoting or late binding changed: %q", project.OnReady.Command)
	}
	for replica := range 2 {
		worker := project.Processes[fmt.Sprintf("worker-%d", replica)]
		if worker.Command != "echo expanded" || len(worker.Environment) != 1 || worker.Environment[0] != "NAME=expanded" {
			t.Errorf("ordinary aliased command/environment expansion changed: %+v", worker)
		}
		if worker.OnStart.Command != "echo ${PC_EVENT}" {
			t.Errorf("hook metadata expanded before YAML parsing: %q", worker.OnStart.Command)
		}
		if worker.OnReady.Command != "echo ${PC_PROCESS_NAME}" {
			t.Errorf("aliased hook was not preserved: %q", worker.OnReady.Command)
		}
		if worker.OnExit.Command != "echo ${PC_EVENT:-'quoted'}" {
			t.Errorf("hook quoting changed: %q", worker.OnExit.Command)
		}
	}
}

func TestStructuralExpansionPreservesHookIndentationAndFlowDelimiters(t *testing.T) {
	t.Setenv("CONFIG_ENV", "MODE=dev")
	t.Setenv("PC_EVENT", "A=1,B=2")
	project, err := loadHookFixture(t, `is_strict: true
environment: [${CONFIG_ENV}]
processes:
  worker:
    command: echo ok
    environment: [${PC_EVENT}]
    on_start:
      command: |
        echo "${PC_EVENT:-first
        second}"
`)
	if err != nil {
		t.Fatal(err)
	}
	worker := project.Processes["worker"]
	if len(worker.Environment) != 2 || worker.Environment[0] != "A=1" || worker.Environment[1] != "B=2" {
		t.Errorf("ordinary flow substitution lost its delimiters: %v", worker.Environment)
	}
	want := "echo \"${PC_EVENT:-first\nsecond}\"\n"
	if worker.OnStart.Command != want {
		t.Errorf("hook block indentation changed: got %q, want %q", worker.OnStart.Command, want)
	}
}
