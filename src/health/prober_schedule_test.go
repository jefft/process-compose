package health

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/f1bonacc1/go-health/v2"
	"github.com/f1bonacc1/process-compose/src/command"
)

// tolerance absorbs scheduler jitter on loaded CI runners while staying well
// below the 1s minimum probe period, so a wrong interval choice is still caught.
const tolerance = 500 * time.Millisecond

type fakeChecker struct {
	mu    sync.Mutex
	calls []time.Time
	// fail reports whether the n-th (1-based) check should fail.
	fail func(n int) bool
}

func (f *fakeChecker) Status() (any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, time.Now())
	n := len(f.calls)
	f.mu.Unlock()

	if f.fail(n) {
		return map[string]string{"exit_code": "1"}, errors.New("check failed")
	}
	return map[string]string{"exit_code": "0"}, nil
}

func (f *fakeChecker) snapshot() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.calls...)
}

func newTestProber(t *testing.T, probe Probe, checker health.ICheckable, onEnd func(bool, bool, string, any)) *Prober {
	t.Helper()
	probe.Exec = &ExecProbe{Command: "true"}
	p, err := New(t.Name(), probe, nil, *command.DefaultShellConfig(), onEnd)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p.checker = checker
	return p
}

// assertSchedule asserts the exact number of checks and when each one ran,
// measured from the first check. Offsets are absolute rather than gap-to-gap:
// time.Ticker schedules against a fixed origin, so one late delivery must not
// cascade into a failure for every later check.
func assertSchedule(t *testing.T, calls []time.Time, want []time.Duration) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("got %d checks, want %d", len(calls), len(want))
	}
	for i, w := range want {
		got := calls[i].Sub(calls[0])
		if got < w-tolerance || got > w+tolerance {
			t.Errorf("check %d ran at +%v, want ~+%v", i+1, got.Round(time.Millisecond), w)
		}
	}
}

func TestProber_StartupPeriodAppliesUntilFirstSuccess(t *testing.T) {
	t.Parallel()
	// Fail twice, then succeed. The first success must switch the poll interval
	// to period_seconds immediately, not one startup interval later.
	checker := &fakeChecker{fail: func(n int) bool { return n < 3 }}
	p := newTestProber(t, Probe{
		StartupPeriodSeconds: 1,
		PeriodSeconds:        5,
		FailureThreshold:     100,
	}, checker, func(bool, bool, string, any) {})

	p.Start()
	// t=0 fail, t=1 fail, t=2 ok, next check not before t=7.
	time.Sleep(3500 * time.Millisecond)
	p.Stop()

	assertSchedule(t, checker.snapshot(), []time.Duration{0, time.Second, 2 * time.Second})
}

func TestProber_SwitchesToPeriodAfterImmediateSuccess(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{fail: func(int) bool { return false }}
	p := newTestProber(t, Probe{
		StartupPeriodSeconds: 1,
		PeriodSeconds:        4,
		FailureThreshold:     100,
	}, checker, func(bool, bool, string, any) {})

	p.Start()
	// The check at t=0 succeeds, so the next one is due at t=4, not at t=1:
	// the startup interval must not survive the check that ends startup.
	time.Sleep(2500 * time.Millisecond)
	p.Stop()

	if got := len(checker.snapshot()); got != 1 {
		t.Errorf("got %d checks in 2.5s, want 1", got)
	}
}

func TestProber_UnhealthyPeriodAppliesAfterFailure(t *testing.T) {
	t.Parallel()
	// Succeed once, then fail forever.
	checker := &fakeChecker{fail: func(n int) bool { return n > 1 }}
	p := newTestProber(t, Probe{
		StartupPeriodSeconds:   1,
		PeriodSeconds:          1,
		UnhealthyPeriodSeconds: 2,
		FailureThreshold:       100,
	}, checker, func(bool, bool, string, any) {})

	p.Start()
	// t=0 ok, t=1 fail, t=3 fail, next check not before t=5.
	time.Sleep(4 * time.Second)
	p.Stop()

	assertSchedule(t, checker.snapshot(), []time.Duration{0, time.Second, 3 * time.Second})
}

func TestProber_UnhealthyPeriodDefaultsToPeriod(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{fail: func(n int) bool { return n > 1 }}
	p := newTestProber(t, Probe{
		StartupPeriodSeconds: 1,
		PeriodSeconds:        1,
		FailureThreshold:     100,
	}, checker, func(bool, bool, string, any) {})

	p.Start()
	time.Sleep(2500 * time.Millisecond)
	p.Stop()

	assertSchedule(t, checker.snapshot(), []time.Duration{0, time.Second, 2 * time.Second})
}

func TestProber_FatalReportedOncePerFailureStreak(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{fail: func(int) bool { return true }}

	var mu sync.Mutex
	fatals := 0
	p := newTestProber(t, Probe{
		StartupPeriodSeconds: 1,
		PeriodSeconds:        1,
		FailureThreshold:     2,
	}, checker, func(_, isFatal bool, _ string, _ any) {
		if isFatal {
			mu.Lock()
			fatals++
			mu.Unlock()
		}
	})

	p.Start()
	// t=0, t=1, t=2 all fail; only the second breaches the threshold. Later
	// checks keep failing without re-reporting, so a longer wait is still valid.
	time.Sleep(3500 * time.Millisecond)
	p.Stop()

	if got := len(checker.snapshot()); got < 3 {
		t.Fatalf("got %d checks, want at least 3", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if fatals != 1 {
		t.Errorf("fatal reported %d times, want 1", fatals)
	}
}

func TestProber_FailureStreakResetsOnSuccess(t *testing.T) {
	t.Parallel()
	// Two failures, a success, then two more -> two distinct streaks, two fatals.
	checker := &fakeChecker{fail: func(n int) bool { return n != 3 }}

	var mu sync.Mutex
	fatals := 0
	p := newTestProber(t, Probe{
		StartupPeriodSeconds: 1,
		PeriodSeconds:        1,
		FailureThreshold:     2,
	}, checker, func(_, isFatal bool, _ string, _ any) {
		if isFatal {
			mu.Lock()
			fatals++
			mu.Unlock()
		}
	})

	p.Start()
	time.Sleep(4600 * time.Millisecond)
	p.Stop()

	mu.Lock()
	defer mu.Unlock()
	if fatals != 2 {
		t.Errorf("fatal reported %d times, want 2", fatals)
	}
}

func TestProber_RestartDoesNotLeakMonitoringLoop(t *testing.T) {
	t.Parallel()
	// A restarted process calls Start() again on the same Prober without an
	// intervening Stop(). The previous loop must not survive it.
	checker := &fakeChecker{fail: func(int) bool { return false }}
	p := newTestProber(t, Probe{
		StartupPeriodSeconds: 1,
		PeriodSeconds:        1,
		FailureThreshold:     100,
	}, checker, func(bool, bool, string, any) {})

	p.Start()
	time.Sleep(1500 * time.Millisecond)
	p.Start()
	time.Sleep(500 * time.Millisecond)
	p.Stop()

	before := len(checker.snapshot())
	if before == 0 {
		t.Fatal("prober never ran a check")
	}
	time.Sleep(2500 * time.Millisecond)
	if after := len(checker.snapshot()); after != before {
		t.Errorf("prober kept checking after Stop: %d checks -> %d", before, after)
	}
}

func TestProber_StopIsIdempotentAndRestartable(t *testing.T) {
	t.Parallel()
	checker := &fakeChecker{fail: func(int) bool { return false }}
	p := newTestProber(t, Probe{
		StartupPeriodSeconds: 1,
		PeriodSeconds:        1,
		FailureThreshold:     100,
	}, checker, func(bool, bool, string, any) {})

	p.Stop() // never started
	p.Start()
	time.Sleep(200 * time.Millisecond)
	p.Stop()
	p.Stop()

	before := len(checker.snapshot())
	p.Start()
	time.Sleep(200 * time.Millisecond)
	p.Stop()

	if after := len(checker.snapshot()); after <= before {
		t.Errorf("prober did not restart: %d checks -> %d", before, after)
	}
}

func TestProber_FailureThresholdScaling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		probe    Probe
		interval time.Duration
		want     int64
	}{
		{"steady state uses the raw threshold", Probe{PeriodSeconds: 10, FailureThreshold: 3}, 10 * time.Second, 3},
		{"1s polling preserves the 20s grace", Probe{PeriodSeconds: 10, FailureThreshold: 3}, time.Second, 21},
		{"2s polling preserves the 20s grace", Probe{PeriodSeconds: 10, FailureThreshold: 3}, 2 * time.Second, 11},
		{"indivisible interval rounds attempts up", Probe{PeriodSeconds: 10, FailureThreshold: 3}, 3 * time.Second, 8},
		{"threshold of 1 still fires on the first failure", Probe{PeriodSeconds: 10, FailureThreshold: 1}, time.Second, 1},
		{"slower than period keeps the raw threshold", Probe{PeriodSeconds: 10, FailureThreshold: 3}, 20 * time.Second, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &Prober{probe: tt.probe}
			got := p.failureThresholdFor(tt.interval)
			if got != tt.want {
				t.Fatalf("failureThresholdFor(%v) = %d, want %d", tt.interval, got, tt.want)
			}
			// The invariant the scaling exists to protect: polling faster than
			// period_seconds must never shorten the wall-clock grace.
			grace := time.Duration(got-1) * tt.interval
			steady := time.Duration(tt.probe.FailureThreshold-1) * time.Duration(tt.probe.PeriodSeconds) * time.Second
			if grace < steady {
				t.Errorf("grace %v is shorter than the steady-state grace %v", grace, steady)
			}
		})
	}
}

func TestProber_FastStartupKeepsWallClockGrace(t *testing.T) {
	t.Parallel()
	// period 4s, threshold 2 => give up 4s after the first failure. Polling
	// every second must still give up at ~4s, on the 5th failure, not the 2nd.
	checker := &fakeChecker{fail: func(int) bool { return true }}

	var mu sync.Mutex
	fatals, fatalAtCheck := 0, 0
	var fatalAt time.Time
	p := newTestProber(t, Probe{
		StartupPeriodSeconds: 1,
		PeriodSeconds:        4,
		FailureThreshold:     2,
	}, checker, func(_, isFatal bool, _ string, _ any) {
		if !isFatal {
			return
		}
		n := len(checker.snapshot())
		mu.Lock()
		fatals++
		fatalAtCheck = n
		fatalAt = time.Now()
		mu.Unlock()
	})

	start := time.Now()
	p.Start()
	time.Sleep(5500 * time.Millisecond)
	p.Stop()

	mu.Lock()
	defer mu.Unlock()
	if fatals != 1 {
		t.Fatalf("fatal reported %d times, want 1", fatals)
	}
	if fatalAtCheck != 5 {
		t.Errorf("fatal reported on check %d, want 5", fatalAtCheck)
	}
	if elapsed := fatalAt.Sub(start); elapsed < 4*time.Second-tolerance || elapsed > 4*time.Second+tolerance {
		t.Errorf("gave up after %v, want ~4s (the steady-state grace)", elapsed)
	}
}
