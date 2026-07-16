package health

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/f1bonacc1/go-health/v2"
	"github.com/f1bonacc1/go-health/v2/checkers"
	"github.com/f1bonacc1/process-compose/src/command"
	"github.com/rs/zerolog/log"
)

type Prober struct {
	probe          Probe
	name           string
	onCheckEndFunc func(bool, bool, string, any)
	env            []string
	shellConfig    command.ShellConfig
	checker        health.ICheckable

	// mu guards the monitoring loop lifecycle and the health tracking state.
	mu sync.Mutex
	// stopCh is non-nil while a monitoring loop is active. Closing it stops
	// that loop; each loop captures its own channel, so a loop started by a
	// later Start() is unaffected by the teardown of its predecessor.
	stopCh              chan struct{}
	wasEverHealthy      bool
	isHealthy           bool
	consecutiveFailures int64
}

func New(name string, probe Probe, env []string, shellConfig command.ShellConfig, onCheckEnd func(bool, bool, string, any)) (*Prober, error) {
	probe.ValidateAndSetDefaults()
	p := &Prober{
		probe:          probe,
		name:           name,
		onCheckEndFunc: onCheckEnd,
		env:            env,
		shellConfig:    shellConfig,
	}

	var checker health.ICheckable
	var err error
	switch {
	case probe.Exec != nil:
		checker, err = p.getExecChecker()
	case probe.HttpGet != nil:
		checker, err = p.getHttpChecker()
	default:
		return nil, fmt.Errorf("no probes [http_get, exec] configured for %s", name)
	}
	if err != nil {
		return nil, err
	}
	p.checker = checker
	return p, nil
}

// Start begins monitoring. Calling it on an already monitoring Prober (a
// restarted process reuses its Prober) stops the previous loop and resets the
// health state, so the new process instance is probed from scratch.
func (p *Prober) Start() {
	p.mu.Lock()
	if p.stopCh != nil {
		close(p.stopCh)
	}
	stopCh := make(chan struct{})
	p.stopCh = stopCh
	p.wasEverHealthy = false
	p.isHealthy = false
	p.consecutiveFailures = 0
	p.mu.Unlock()

	go func() {
		if p.probe.InitialDelay > 0 {
			select {
			case <-time.After(time.Duration(p.probe.InitialDelay) * time.Second):
			case <-stopCh:
				return
			}
		}
		log.Debug().Msgf("%s started monitoring", p.name)
		p.runLoop(stopCh)
		log.Debug().Msgf("%s stopped monitoring", p.name)
	}()
}

func (p *Prober) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopCh == nil {
		return
	}
	close(p.stopCh)
	p.stopCh = nil
}

func (p *Prober) runLoop(stopCh <-chan struct{}) {
	// Run the first check immediately, then poll at the state dependent interval.
	p.runCheck(stopCh)

	interval := p.nextInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.runCheck(stopCh)
			if next := p.nextInterval(); next != interval {
				interval = next
				ticker.Reset(interval)
			}
		case <-stopCh:
			return
		}
	}
}

// nextInterval returns the interval to wait before the next check, based on the
// health state recorded by the check that just ran.
func (p *Prober) nextInterval() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.intervalLocked()
}

// intervalLocked returns the poll interval for the probe's current health
// phase. Caller must hold p.mu.
func (p *Prober) intervalLocked() time.Duration {
	switch {
	case !p.wasEverHealthy:
		return time.Duration(p.probe.StartupPeriodSeconds) * time.Second
	case !p.isHealthy:
		return time.Duration(p.probe.UnhealthyPeriodSeconds) * time.Second
	default:
		return time.Duration(p.probe.PeriodSeconds) * time.Second
	}
}

// failureThresholdFor returns the number of consecutive failures that declares
// the probe fatally failed when polling at interval.
//
// FailureThreshold is expressed in PeriodSeconds units: polling faster than
// PeriodSeconds buys proportionally more attempts, so the wall-clock grace
// before giving up stays at (FailureThreshold-1) * PeriodSeconds no matter
// which phase the probe is in. Without this, a short startup_period_seconds
// would shrink the grace given to a slow-starting process and restart it.
// Intervals slower than PeriodSeconds keep the raw threshold; they already
// grant a longer grace, and shrinking it would be the same trap in reverse.
func (p *Prober) failureThresholdFor(interval time.Duration) int64 {
	threshold := int64(p.probe.FailureThreshold)
	period := time.Duration(p.probe.PeriodSeconds) * time.Second
	if interval <= 0 || interval >= period {
		return threshold
	}
	grace := time.Duration(threshold-1) * period
	// ceil(grace/interval) additional attempts, plus the one that opens the streak.
	return int64((grace+interval-1)/interval) + 1
}

func (p *Prober) runCheck(stopCh <-chan struct{}) {
	details, err := p.checker.Status()
	isOk := err == nil
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	p.mu.Lock()
	if isOk {
		p.consecutiveFailures = 0
		p.wasEverHealthy = true
		p.isHealthy = true
	} else {
		p.consecutiveFailures++
		p.isHealthy = false
	}
	// Report the breach exactly once per failure streak: the fatal handlers
	// (daemon stopped notification, internal stop) are not idempotent. The
	// phase cannot change while a streak lasts - only a success leaves it, and
	// that resets the counter - so the threshold is stable across the streak.
	fatal := !isOk && p.consecutiveFailures == p.failureThresholdFor(p.intervalLocked())
	p.mu.Unlock()

	select {
	case <-stopCh:
		return
	default:
	}
	p.onCheckEndFunc(isOk, fatal, errMsg, details)
}

func (p *Prober) getHttpChecker() (health.ICheckable, error) {
	httpGet := p.probe.HttpGet
	url, err := httpGet.getUrl()
	if err != nil {
		return nil, err
	}

	config := &checkers.HTTPConfig{
		URL:        url,
		Timeout:    time.Duration(p.probe.TimeoutSeconds) * time.Second,
		StatusCode: httpGet.StatusCode,
	}

	if len(httpGet.Headers) > 0 {
		config.Headers = http.Header{}
		for k, v := range httpGet.Headers {
			config.Headers.Set(k, v)
		}
	}

	checker, err := checkers.NewHTTP(config)
	if err != nil {
		return nil, err
	}
	return checker, nil
}

func (p *Prober) getExecChecker() (health.ICheckable, error) {
	return &execChecker{
		name:        p.name,
		command:     p.probe.Exec.Command,
		timeout:     p.probe.TimeoutSeconds,
		workingDir:  p.probe.Exec.WorkingDir,
		env:         p.env,
		shellConfig: p.shellConfig,
	}, nil
}
