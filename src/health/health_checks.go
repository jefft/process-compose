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

const (
	OK     = "ok"
	failed = "failed"
)

type Prober struct {
	probe          Probe
	name           string
	onCheckEndFunc func(bool, bool, string, any)
	env            []string
	shellConfig    command.ShellConfig

	checker health.ICheckable

	// mu protects stopCh, stopped, and all health tracking fields.
	mu                  sync.Mutex
	stopCh              chan struct{}
	stopped             bool
	wasEverHealthy      bool
	isHealthy           bool
	consecutiveFailures int64
	firstFailureTime    time.Time
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
	if probe.Exec != nil {
		checker, err = p.getExecChecker()
	} else if probe.HttpGet != nil {
		checker, err = p.getHttpChecker()
	} else {
		return nil, fmt.Errorf("no probes [http_get, exec] configured for %s", name)
	}
	if err != nil {
		return nil, err
	}
	p.checker = checker
	return p, nil
}

func (p *Prober) Start() {
	p.mu.Lock()
	p.stopCh = make(chan struct{})
	p.stopped = false
	p.wasEverHealthy = false
	p.isHealthy = false
	p.consecutiveFailures = 0
	p.firstFailureTime = time.Time{}
	stopCh := p.stopCh // capture under lock for the goroutine
	p.mu.Unlock()

	go func() {
		if p.probe.InitialDelay > 0 {
			select {
			case <-time.After(time.Duration(p.probe.InitialDelay) * time.Second):
			case <-stopCh:
				return
			}
		}

		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()

		p.runLoop(stopCh)
		log.Debug().Msgf("%s stopped monitoring", p.name)
	}()
	log.Debug().Msgf("%s started monitoring", p.name)
}

func (p *Prober) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return
	}
	p.stopped = true
	if p.stopCh != nil {
		close(p.stopCh)
	}
}

// currentInterval returns the interval that should be used for the next wait
// based on the probe's current health state. Caller must hold p.mu.
func (p *Prober) currentInterval() time.Duration {
	if !p.wasEverHealthy {
		return time.Duration(p.probe.StartupPeriodSeconds) * time.Second
	}
	if !p.isHealthy && p.probe.UnhealthyPeriodSeconds != p.probe.PeriodSeconds {
		return time.Duration(p.probe.UnhealthyPeriodSeconds) * time.Second
	}
	return time.Duration(p.probe.PeriodSeconds) * time.Second
}

func (p *Prober) runLoop(stopCh chan struct{}) {
	p.mu.Lock()
	interval := p.currentInterval()
	p.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run first check immediately.
	p.runCheck()

	for {
		select {
		case <-ticker.C:
			p.runCheck()
			// After the check, recalculate interval for the next wait.
			p.mu.Lock()
			newInterval := p.currentInterval()
			p.mu.Unlock()
			if newInterval != interval {
				ticker.Reset(newInterval)
				interval = newInterval
			}
		case <-stopCh:
			return
		}
	}
}

func (p *Prober) runCheck() {
	data, err := p.checker.Status()

	state := &health.State{
		Name:      p.name,
		Status:    OK,
		Details:   data,
		CheckTime: time.Now(),
	}

	isOk := true
	if err != nil {
		state.Status = failed
		state.Err = err.Error()
		isOk = false
	}

	// Track contiguous failures and state transitions.
	p.mu.Lock()
	if !isOk {
		if p.firstFailureTime.IsZero() {
			p.firstFailureTime = time.Now()
		}
		p.consecutiveFailures++
		state.ContiguousFailures = p.consecutiveFailures
		state.TimeOfFirstFailure = p.firstFailureTime
	} else {
		p.consecutiveFailures = 0
		p.firstFailureTime = time.Time{}
	}

	consecutiveFailures := p.consecutiveFailures

	if isOk {
		p.wasEverHealthy = true
		p.isHealthy = true
	} else {
		p.isHealthy = false
	}
	stopped := p.stopped
	p.mu.Unlock()

	fatal := consecutiveFailures >= int64(p.probe.FailureThreshold)

	if !stopped {
		p.onCheckEndFunc(isOk, fatal, state.Err, state.Details)
	}
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
