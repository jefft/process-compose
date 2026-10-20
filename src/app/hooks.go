package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/f1bonacc1/process-compose/src/types"
)

// executeHook runs a notification independently of the service's cancellation
// context. Callers serialize notifications and supply their own lifetime context.
func executeHook(ctx context.Context, hook *types.HookConfig, shell command.ShellConfig, env []string, dir string, metadata map[string]any, output func(string)) {
	if hook == nil {
		return
	}
	timeout := hook.TimeoutSeconds
	if timeout == 0 {
		timeout = types.DefaultHookTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if output == nil {
		output = func(string) {}
	}
	hookEnv, err := hookEnvironment(env, metadata)
	if err != nil {
		output(fmt.Sprintf("Hook %v: %v", metadata["event"], err))
		return
	}
	cmd := exec.CommandContext(ctx, shell.ShellCommand, shell.ShellArgument, hook.Command)
	cmd.Env = hookEnv
	cmd.Dir = dir
	// A background descendant must not keep Wait blocked by inherited pipes.
	cmd.WaitDelay = time.Second
	configureHookCommand(cmd)
	writer := &hookLogWriter{output: output}
	cmd.Stdout, cmd.Stderr = writer, writer
	err = cmd.Run()
	cleanupHookCommand(cmd)
	writer.flush()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		output(fmt.Sprintf("Hook %v failed: %v", metadata["event"], err))
	}
}

var hookReservedEnv = map[string]struct{}{
	"PC_EVENT": {}, "PC_EVENT_TIME": {}, "PC_EVENT_JSON": {},
	"PC_PROJECT_NAME": {}, "PC_PROJECT_DIR": {},
	"PC_PROCESS_NAME": {}, "PC_PROCESS_NAMESPACE": {}, "PC_PROCESS_PID": {}, "PC_RESTART_COUNT": {},
	"PC_TCP_PORTS": {}, "PC_UDP_PORTS": {}, "PC_PORTS_STATUS": {},
	"PC_PREVIOUS_TCP_PORTS": {}, "PC_PREVIOUS_UDP_PORTS": {},
	"PC_EXIT_CODE": {}, "PC_EXIT_SIGNAL": {}, "PC_STOP_REASON": {},
}

func hookEnvironment(env []string, metadata map[string]any) ([]string, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(env)+len(hookReservedEnv))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if _, reserved := hookReservedEnv[key]; !reserved {
			result = append(result, entry)
		}
	}
	appendFields := func(fields map[string]any, names ...[2]string) {
		for _, name := range names {
			if value, ok := fields[name[0]]; ok {
				result = append(result, name[1]+"="+fmt.Sprint(value))
			}
		}
	}
	appendFields(metadata,
		[2]string{"event", "PC_EVENT"}, [2]string{"event_time", "PC_EVENT_TIME"},
		[2]string{"project_name", "PC_PROJECT_NAME"}, [2]string{"project_dir", "PC_PROJECT_DIR"},
		[2]string{"exit_code", "PC_EXIT_CODE"}, [2]string{"exit_signal", "PC_EXIT_SIGNAL"}, [2]string{"stop_reason", "PC_STOP_REASON"},
	)
	if process, ok := metadata["process"].(map[string]any); ok {
		appendFields(process,
			[2]string{"name", "PC_PROCESS_NAME"}, [2]string{"namespace", "PC_PROCESS_NAMESPACE"},
			[2]string{"pid", "PC_PROCESS_PID"}, [2]string{"restart_count", "PC_RESTART_COUNT"},
		)
	}
	if ports, ok := metadata["ports"].(map[string]any); ok {
		result = append(result, "PC_TCP_PORTS="+hookPortString(ports["tcp"]), "PC_UDP_PORTS="+hookPortString(ports["udp"]))
		appendFields(ports, [2]string{"status", "PC_PORTS_STATUS"})
	}
	if ports, ok := metadata["previous_ports"].(map[string]any); ok {
		result = append(result, "PC_PREVIOUS_TCP_PORTS="+hookPortString(ports["tcp"]), "PC_PREVIOUS_UDP_PORTS="+hookPortString(ports["udp"]))
	}
	return append(result, "PC_EVENT_JSON="+string(encoded)), nil
}

func hookPortString(value any) string {
	ports, ok := value.([]uint16)
	if !ok {
		return ""
	}
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = strconv.Itoa(int(port))
	}
	return strings.Join(parts, " ")
}

// hookLogWriter bounds partial lines, while keeping output streaming and ordered
// when the command's stdout and stderr are copied concurrently.
type hookLogWriter struct {
	mu      sync.Mutex
	partial string
	output  func(string)
}

func (w *hookLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	length := len(data)
	for len(data) > 0 {
		end := slices.Index(data, byte('\n'))
		if end < 0 {
			end = len(data)
		}
		chunk := min(end, 64*1024-len(w.partial))
		w.partial += string(data[:chunk])
		data = data[chunk:]
		if chunk == end && len(data) > 0 && data[0] == '\n' {
			data = data[1:]
			w.output(strings.TrimSuffix(w.partial, "\r"))
			w.partial = ""
		} else if len(w.partial) == 64*1024 {
			w.output(w.partial)
			w.partial = ""
		}
	}
	return length, nil
}

func (w *hookLogWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.partial != "" {
		w.output(w.partial)
		w.partial = ""
	}
}
