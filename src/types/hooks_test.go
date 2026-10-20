package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProcessCompareLifecycleHooks(t *testing.T) {
	for _, field := range []string{"on_start", "on_ready", "on_exit", "on_ports_changed"} {
		t.Run(field, func(t *testing.T) {
			setHook := func(p *ProcessConfig, hook *HookConfig) {
				switch field {
				case "on_start":
					p.OnStart = hook
				case "on_ready":
					p.OnReady = hook
				case "on_exit":
					p.OnExit = hook
				case "on_ports_changed":
					p.OnPortsChanged = hook
				}
			}
			var a, b ProcessConfig
			setHook(&a, &HookConfig{Command: "echo ready", TimeoutSeconds: 2})
			setHook(&b, &HookConfig{Command: "echo ready", TimeoutSeconds: 2})
			if !a.Compare(&b) {
				t.Fatal("equal hook configurations compare unequal")
			}
			setHook(&b, &HookConfig{Command: "echo changed", TimeoutSeconds: 2})
			if a.Compare(&b) {
				t.Fatal("changed hook command was ignored")
			}
			setHook(&b, &HookConfig{Command: "echo ready", TimeoutSeconds: 3})
			if a.Compare(&b) {
				t.Fatal("changed hook timeout was ignored")
			}
			setHook(&b, nil)
			if a.Compare(&b) {
				t.Fatal("removed hook was ignored")
			}
		})
	}
}

func TestLifecycleHookJSONNames(t *testing.T) {
	proc := ProcessConfig{OnStart: &HookConfig{Command: "echo started", TimeoutSeconds: 3}, OnReady: &HookConfig{Command: "ready"}, OnExit: &HookConfig{Command: "exit"}, OnPortsChanged: &HookConfig{Command: "ports"}}
	data, err := json.Marshal(proc)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"onStart", "onReady", "onExit", "onPortsChanged", "timeoutSeconds"} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("missing JSON key %s: %s", key, data)
		}
	}
}
