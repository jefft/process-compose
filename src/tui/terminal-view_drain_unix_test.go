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
