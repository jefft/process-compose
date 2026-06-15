package app

import (
	"bytes"
	"testing"
)

func TestInterpretKeyEscapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []byte
	}{
		{"plain", "q", []byte("q")},
		{"multi char", "quit", []byte("quit")},
		{"carriage return", `q\r`, []byte("q\r")},
		{"newline", `\n`, []byte("\n")},
		{"tab", `a\tb`, []byte("a\tb")},
		{"ctrl-c hex", `\x03`, []byte{0x03}},
		{"hex uppercase digits", `\xFF`, []byte{0xff}},
		{"hex with capital X", `\X1b`, []byte{0x1b}},
		{"escape", `\e`, []byte{0x1b}},
		{"nul", `\0`, []byte{0x00}},
		{"literal backslash", `a\\b`, []byte(`a\b`)},
		{"unknown escape kept literal", `\z`, []byte(`\z`)},
		{"trailing backslash kept", `q\`, []byte(`q\`)},
		{"incomplete hex kept literal", `\x1`, []byte(`\x1`)},
		{"invalid hex digit kept literal", `\xZZ`, []byte(`\xZZ`)},
		{"mixed", `:q\r`, []byte(":q\r")},
		{"empty", "", []byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := interpretKeyEscapes(tt.in)
			if !bytes.Equal(got, tt.want) {
				t.Errorf("interpretKeyEscapes(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
