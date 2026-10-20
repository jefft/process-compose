package types

import (
	"fmt"
	"strings"
)

// DefaultHookTimeout is the hook execution deadline in seconds when omitted.
const DefaultHookTimeout = 10

// HookConfig runs a notification command without changing process lifecycle state.
type HookConfig struct {
	Command        string `yaml:"command" json:"command" jsonschema:"minLength=1"`
	TimeoutSeconds int    `yaml:"timeout_seconds,omitempty" json:"timeoutSeconds,omitempty" jsonschema:"minimum=0,default=10"`
}

func (h *HookConfig) Validate() error {
	if h == nil {
		return nil
	}
	if strings.TrimSpace(h.Command) == "" {
		return fmt.Errorf("hook command must not be empty")
	}
	if h.TimeoutSeconds < 0 {
		return fmt.Errorf("hook timeout_seconds must not be negative")
	}
	return nil
}

func (h *HookConfig) Clone() *HookConfig {
	if h == nil {
		return nil
	}
	clone := *h
	return &clone
}
