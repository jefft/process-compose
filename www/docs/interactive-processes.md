# Interactive Processes

## Description

Interactive processes allow you to interact directly with a running process through the TUI. This is useful for processes that require user input, such as a REPL, a debugger, or a command-line editor.

When a process is configured as interactive, Process Compose allocates a pseudo-terminal (PTY) for it and allows you to attach to its standard input and output.

## Configuration

To enable interactive mode for a process, set `is_interactive: true` in your `process-compose.yaml` configuration.

```yaml
processes:
  my-interactive-process:
    command: "python3"
    is_interactive: true
```

## Usage

### Attaching to a Process

In the TUI, select the interactive process from the list. Its live output is rendered in the process's pane, the same pane that shows logs for non-interactive processes.

!!! note
    An interactive process is backed by a pseudo-terminal (PTY) and its output is rendered live in its pane. That output is **not** captured into the line log buffer, so `process-compose process logs <name>` (and the REST/WebSocket log API and file logging) will return nothing for it. If you need a process's output collected in the logs, leave it non-interactive. Use `is_interactive` only when you need to interact with the process.

### Switching Focus

To interact with the process, you need to switch focus to the terminal view.

- **Switch Focus**: Press `TAB` to switch focus from the process list/logs to the interactive terminal.
- **Exit Focus**: Press `CTRL+A` followed by `ESC` to release focus from the interactive terminal and return to the TUI navigation.

### Scrolling

To view earlier output of an interactive process:
- **Mouse**: Scroll up with the mouse wheel. This automatically enters **Scroll Mode**.
- **Keyboard**: Press `CTRL+A` (or the configured exit key) to enter **Scroll Mode**, then use `UP`/`DOWN` arrows or `PageUp`/`PageDown` to navigate.

To exit Scroll Mode and return to live output, press `ESC`.

### Keybindings

| Key Combination | Action |
| :--- | :--- |
| `TAB` | Switch focus to the interactive process terminal (Input Mode). |
| `CTRL+A` followed by `ESC` | Exit Input Mode and return to TUI navigation (Normal Mode). |
| `CTRL+A` followed by `UP`/`DOWN` | Enter Scroll Mode and scroll the history. |
| `Mouse Wheel` | Scroll history (enters Scroll Mode automatically). |
| `ESC` (in Scroll Mode) | Exit Scroll Mode and jump to bottom. |

## Sending Keys Programmatically

You can send keystroke(s) to a running interactive process's stdin without attaching to the TUI — useful for scripting input or stopping programs that quit on a key:

```shell
process-compose process send-keys my-interactive-process "q"
```

Control keys can be expressed with escape sequences, e.g. `'\x03'` for `Ctrl-C` or `'q\r'` to send `q` followed by `Enter`.

To make process-compose send keys automatically when stopping a process, use [`shutdown.send_keys`](launcher.md#stopping-interactive-processes-with-keystrokes).
