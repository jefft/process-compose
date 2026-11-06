package types

// HookOutputLine is one hook stdout/stderr line or hook failure notice.
// An empty Process denotes a project-level hook. Message excludes the newline.
type HookOutputLine struct {
	Sequence uint64 `json:"sequence"`
	Process  string `json:"process,omitempty"`
	Event    string `json:"event"`
	Message  string `json:"message"`
}

// HookOutputSnapshot contains retained hook output after a sequence cursor.
// NextSequence is the latest recorded sequence, even when Lines is empty.
type HookOutputSnapshot struct {
	Lines           []HookOutputLine `json:"lines"`
	NextSequence    uint64           `json:"next_sequence"`
	Pending         int              `json:"pending"`
	StartupComplete bool             `json:"startup_complete"`
	Truncated       bool             `json:"truncated"`
}
