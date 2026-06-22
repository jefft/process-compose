package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// TestDeferredTerminalReplaysAtActivatedWidth is a unit-level regression test
// for issue #512. A deferred terminal must not parse output at its 80-column
// placeholder; it must buffer the raw bytes and replay them only once Activate
// supplies the real width, so wrapping reflects the actual pane.
func TestDeferredTerminalReplaysAtActivatedWidth(t *testing.T) {
	const line = 120 // longer than the 80-column placeholder, shorter than 200

	// Wide activation: a 120-char line fits on one row, so it must NOT wrap.
	wide := NewDeferredAnsiTerminal()
	wide.Write([]byte(strings.Repeat("X", line) + "\n"))
	// Nothing is parsed while deferred.
	if c := wide.GetCell(0, 0).Char; c != ' ' {
		t.Fatalf("deferred terminal parsed output before Activate: cell(0,0)=%q", c)
	}
	wide.Activate(200, 24)
	for x := range line {
		if c := wide.GetCell(x, 0).Char; c != 'X' {
			t.Fatalf("at 200 cols col %d of row 0 = %q, want 'X' (line wrapped though it fit)", x, c)
		}
	}
	if c := wide.GetCell(0, 1).Char; c != ' ' {
		t.Fatalf("at 200 cols row 1 is not empty (line wrapped though it fit): %q", c)
	}

	// Narrow activation: the same line must wrap at the activated width (100),
	// not at the 80-column placeholder.
	narrow := NewDeferredAnsiTerminal()
	narrow.Write([]byte(strings.Repeat("X", line) + "\n"))
	narrow.Activate(100, 24)
	if c := narrow.GetCell(99, 0).Char; c != 'X' {
		t.Fatalf("at 100 cols col 99 of row 0 = %q, want 'X' (wrapped before the real width)", c)
	}
	if c := narrow.GetCell(line-100-1, 1).Char; c != 'X' {
		t.Fatalf("at 100 cols the wrapped remainder is missing on row 1")
	}
}

// TestCSIIntermediateBytesNotPrinted guards against a CSI parser bug where
// intermediate bytes (0x20-0x2F) were misclassified as the final byte, ending
// the sequence early and printing its trailing final byte as a literal
// character. The concrete symptom was helix's DECSCUSR cursor-shape sequence
// (ESC [ <n> SP q) leaking a stray 'q' onto the screen on every ':'.
func TestCSIIntermediateBytesNotPrinted(t *testing.T) {
	cases := []struct {
		name string
		seq  string
	}{
		{"DECSCUSR steady bar", "\x1b[6 q"},
		{"DECSCUSR blinking block", "\x1b[1 q"},
		{"DECSCUSR default", "\x1b[ q"},
		{"DECSTR soft reset", "\x1b[!p"},
		{"DECSCA", "\x1b[\"q"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := NewAnsiTerminal(80, 24)
			term.Write([]byte(tc.seq))
			// The sequence must be fully consumed: nothing printed and the
			// parser back in the normal state so following text lands at (0,0).
			if c := term.GetCell(0, 0).Char; c != ' ' {
				t.Fatalf("%s leaked %q onto the screen at (0,0)", tc.name, c)
			}
			term.Write([]byte("Z"))
			if c := term.GetCell(0, 0).Char; c != 'Z' {
				t.Fatalf("%s left the parser in a bad state: text landed as %q, want 'Z'", tc.name, c)
			}
		})
	}
}

func TestAnsiColors(t *testing.T) {
	term := NewAnsiTerminal(80, 24)

	tests := []struct {
		name     string
		input    string
		expected tcell.Color // Expected foreground color
		isBg     bool
	}{
		{"Basic Red", "\x1b[31mX", tcell.PaletteColor(1), false},
		{"Bright Red", "\x1b[91mX", tcell.PaletteColor(9), false},
		{"256 Color 123", "\x1b[38;5;123mX", tcell.PaletteColor(123), false},
		{"Background Blue", "\x1b[44mX", tcell.PaletteColor(4), true},
		{"Bright Background Blue", "\x1b[104mX", tcell.PaletteColor(12), true},
		{"256 Background 200", "\x1b[48;5;200mX", tcell.PaletteColor(200), true},
		{"RGB Foreground", "\x1b[38;2;255;0;0mX", tcell.NewRGBColor(255, 0, 0), false},
		{"RGB Background", "\x1b[48;2;0;255;0mX", tcell.NewRGBColor(0, 255, 0), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset terminal state
			term.currentStyle = tcell.StyleDefault
			term.cursorX = 0
			term.cursorY = 0

			term.Write([]byte(tt.input))
			cell := term.GetCell(0, 0)

			var got tcell.Color
			if tt.isBg {
				_, got, _ = cell.Style.Decompose()
			} else {
				got, _, _ = cell.Style.Decompose()
			}

			if got != tt.expected {
				t.Errorf("expected color %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestUtf8Rendering(t *testing.T) {
	term := NewAnsiTerminal(80, 24)

	// Test string with mixed ASCII and multi-byte UTF-8 characters
	// "Hi 🌍" -> 'H', 'i', ' ', '🌍'
	input := "Hi 🌍"
	term.Write([]byte(input))

	// 'H'
	c0 := term.GetCell(0, 0)
	if c0.Char != 'H' {
		t.Errorf("expected 'H', got %c", c0.Char)
	}

	// 'i'
	c1 := term.GetCell(1, 0)
	if c1.Char != 'i' {
		t.Errorf("expected 'i', got %c", c1.Char)
	}

	// ' '
	c2 := term.GetCell(2, 0)
	if c2.Char != ' ' {
		t.Errorf("expected ' ', got %c", c2.Char)
	}

	// '🌍' (Earth Globe Europe-Africa)
	c3 := term.GetCell(3, 0)
	if c3.Char != '🌍' {
		t.Errorf("expected '🌍', got %c", c3.Char)
	}
}
