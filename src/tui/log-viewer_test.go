package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/rivo/tview"
)

func TestEscapeForAnsiWriter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "ANSI reset before literal bracket not corrupted",
			input: "\x1b[0m] after",
			want:  "\x1b[0m] after",
		},
		{
			name:  "literal tview-like tag is escaped",
			input: "[ButThisWont] visible",
			want:  "[ButThisWont[] visible",
		},
		{
			name:  "structured log with ANSI colors and brackets",
			input: "\x1b[2m2026-01-01T00:00:00Z\x1b[0m [\x1b[32m\x1b[1minfo     \x1b[0m] \x1b[1msome log message\x1b[0m [\x1b[34mmyapp\x1b[0m] \x1b[36mkey\x1b[0m=\x1b[35mvalue\x1b[0m",
			want:  "\x1b[2m2026-01-01T00:00:00Z\x1b[0m [\x1b[32m\x1b[1minfo     \x1b[0m] \x1b[1msome log message\x1b[0m [\x1b[34mmyapp\x1b[0m] \x1b[36mkey\x1b[0m=\x1b[35mvalue\x1b[0m",
		},
		{
			name:  "ANSI sequences preserved",
			input: "\x1b[31mred\x1b[0m",
			want:  "\x1b[31mred\x1b[0m",
		},
		{
			name:  "multiple literal tags escaped",
			input: "[red] and [blue]",
			want:  "[red[] and [blue[]",
		},
		{
			name:  "brackets with spaces not escaped",
			input: "[not a tag because of spaces]",
			want:  "[not a tag because of spaces]",
		},
		{
			name:  "ANSI color followed immediately by literal tag",
			input: "\x1b[0m[SomeTag]",
			want:  "\x1b[0m[SomeTag[]",
		},
		{
			name:  "hex color tag escaped",
			input: "[#ff0000]text",
			want:  "[#ff0000[]text",
		},
		{
			name:  "reset tag escaped",
			input: "[-]reset",
			want:  "[-[]reset",
		},
		{
			name:  "compound style tag escaped",
			input: "[red:-:b]bold red",
			want:  "[red:-:b[]bold red",
		},
		{
			name:  "brackets with underscores not escaped",
			input: "[some_var]",
			want:  "[some_var]",
		},
		{
			name:  "region tag with dot escaped (issue #515)",
			input: `{"headers": {"X-Forwarded-Host": ["example.com"]}}`,
			want:  `{"headers": {"X-Forwarded-Host": ["example.com"[]}}`,
		},
		{
			name:  "region tag with space escaped",
			input: `["some region"]`,
			want:  `["some region"[]`,
		},
		{
			name:  "region tag with underscore escaped",
			input: `["some_region"]`,
			want:  `["some_region"[]`,
		},
		{
			name:  "region close tag escaped",
			input: `[""]`,
			want:  `[""[]`,
		},
		{
			name:  "pre-escaped region tag stays literal",
			input: `["abc"[]`,
			want:  `["abc"[[]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeForAnsiWriter(tt.input)
			if got != tt.want {
				t.Errorf("escapeForAnsiWriter(%q)\n got  %q\n want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestEscapeForAnsiWriterRendering verifies the full ANSI-mode log path:
// escapeForAnsiWriter -> tview.ANSIWriter -> TextView with dynamic colors and
// regions enabled (the LogView configuration). The visible text must equal the
// input with ANSI sequences stripped — nothing swallowed as a tag, no bracket
// artifacts added.
func TestEscapeForAnsiWriterRendering(t *testing.T) {
	ansiSgrPattern := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	lines := []string{
		// issue #515
		`{"msg": "handled request", "headers": {"X-Forwarded-Host": ["example.com"]}}`,
		// issue #449 (structlog)
		"\x1b[2m2026-01-01T00:00:00Z\x1b[0m [\x1b[32m\x1b[1minfo     \x1b[0m] \x1b[1msome log message\x1b[0m [\x1b[34mmyapp\x1b[0m] \x1b[36mkey\x1b[0m=\x1b[35mvalue\x1b[0m",
		`["example.com"]`,
		`["with space"]`,
		`["under_score"]`,
		`["a,b;c:d-e.f"]`,
		`[""]`,
		`[red] and [blue]`,
		`[#ff0000]text`,
		`[-]reset`,
		`[red:-:b]bold`,
		`[not a tag because of spaces]`,
		`[some_var]`,
		`array[0] = [1]`,
		`[red[]`,
		`["abc"[]`,
	}
	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			textView := tview.NewTextView().SetDynamicColors(true).SetRegions(true)
			writer := tview.ANSIWriter(textView)
			_, _ = fmt.Fprintln(writer, escapeForAnsiWriter(line))
			got := strings.TrimRight(textView.GetText(true), "\n")
			want := ansiSgrPattern.ReplaceAllString(line, "")
			if got != want {
				t.Errorf("rendered text mismatch for %q\n got  %q\n want %q", line, got, want)
			}
		})
	}
}
