//go:build !windows

package tui

import (
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/rivo/tview"
)

// TestEnsureDrainingUnblocksUnfocusedPty is a regression test for issue #508.
//
// Interactive processes get a PTY whose only running-time consumer is the TUI
// terminal view. Before the fix, that consumer was started lazily and only for
// the currently-selected pane, so an unfocused interactive process blocked on
// write() once the kernel PTY buffer (~16KB on Linux) filled — never reaching
// readiness and stalling any dependents.
//
// EnsureDraining must drain the master end continuously without the pane ever
// being focused or drawn. We prove that by writing far more than any plausible
// kernel buffer to the slave end and asserting the writes complete: 256KB
// cannot be written into a ~16KB buffer unless something is draining it.
func TestEnsureDrainingUnblocksUnfocusedPty(t *testing.T) {
	ptmx, tty, err := pty.Open() // ptmx = master (read by the TUI), tty = slave (process stdout)
	if err != nil {
		t.Fatalf("failed to open pty: %v", err)
	}
	t.Cleanup(func() {
		_ = tty.Close()  // EOF the master so the drain goroutine exits
		_ = ptmx.Close() // then close the master
	})

	// A TerminalView whose pane is never selected or drawn. The drain goroutine
	// never calls app.Draw() because t.pty != ptmx for an unfocused PTY, so an
	// un-run Application is fine here.
	tv := NewTerminalView(tview.NewApplication())
	tv.EnsureDraining(ptmx)

	const payload = 256 * 1024 // >> any plausible kernel PTY buffer
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		for i := range buf {
			buf[i] = 'x'
		}
		for written := 0; written < payload; {
			n, werr := tty.Write(buf)
			if werr != nil {
				done <- werr
				return
			}
			written += n
		}
		done <- nil
	}()

	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("writing to pty slave failed: %v", werr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write to unfocused PTY blocked — EnsureDraining did not drain it (issue #508)")
	}

	// EnsureDraining must be idempotent: a second call (e.g. the next refresh
	// tick, or the pane later being focused) must not start a second reader.
	tv.lock.Lock()
	readers := 0
	for range tv.activeReaders {
		readers++
	}
	tv.lock.Unlock()
	tv.EnsureDraining(ptmx)
	tv.lock.Lock()
	readersAfter := 0
	for range tv.activeReaders {
		readersAfter++
	}
	tv.lock.Unlock()
	if readersAfter != readers {
		t.Fatalf("EnsureDraining is not idempotent: active readers went from %d to %d", readers, readersAfter)
	}
}

// TestEnsureDrainingSizesEmulatorToPane is a regression test for issue #512.
//
// The child process just writes raw bytes; this terminal's emulator performs
// the line wrapping. If the emulator is created at the default 80 columns,
// output produced before the pane is focused/resized gets wrapped at 80 and
// baked into history — AnsiTerminal.Resize does not reflow it afterwards, so it
// stays mis-wrapped until enough new lines scroll it off ("fixes itself after a
// couple of lines"). EnsureDraining must size the emulator (and the PTY) to the
// actual pane width so output is wrapped correctly from the first line.
func TestEnsureDrainingSizesEmulatorToPane(t *testing.T) {
	ptmx, tty, err := pty.Open() // ptmx = master (read by the TUI), tty = slave (process stdout)
	if err != nil {
		t.Fatalf("failed to open pty: %v", err)
	}
	t.Cleanup(func() {
		_ = tty.Close()
		_ = ptmx.Close()
	})

	tv := NewTerminalView(tview.NewApplication())
	// Lay the pane out wider than the 80-column default. The box has a border,
	// so the inner (usable) size is the outer size minus 1 on each side.
	const innerWidth, innerHeight = 120, 30
	tv.SetRect(0, 0, innerWidth+2, innerHeight+2)

	tv.EnsureDraining(ptmx)

	tv.lock.Lock()
	term, ok := tv.terminals[ptmx]
	tv.lock.Unlock()
	if !ok {
		t.Fatal("EnsureDraining did not create a terminal for the PTY")
	}

	term.lock.Lock()
	gotWidth := term.width
	term.lock.Unlock()
	if gotWidth != innerWidth {
		t.Fatalf("emulator width = %d, want pane width %d (issue #512)", gotWidth, innerWidth)
	}

	// The PTY winsize must match too, so curses apps started while the pane is
	// unfocused query the right size instead of the 80x24 default.
	_, cols, err := pty.Getsize(ptmx)
	if err != nil {
		t.Fatalf("failed to get pty size: %v", err)
	}
	if cols != innerWidth {
		t.Fatalf("pty cols = %d, want %d (issue #512)", cols, innerWidth)
	}
}
