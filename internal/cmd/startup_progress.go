package cmd

import (
	"log/slog"
	"sync"
	"time"
)

// startupProgress names the step startup is in and warns when one runs long,
// so a slow or stuck start says what it is waiting on. Integration runs on
// CI and WSL occasionally stalled after "multi-agent orchestration enabled"
// with nothing in the log to show which step; Windows has no SIGQUIT stack
// dump to fall back on.
type startupProgress struct {
	mu     sync.Mutex
	log    *slog.Logger
	name   string
	since  time.Time
	warned bool
	stop   chan struct{}
}

// slowStartupStep is how long a step may run before it is reported.
var slowStartupStep = 3 * time.Second

var bootSteps = &startupProgress{}

// begin starts watching; finish stops. Both are safe to call once per run.
func (p *startupProgress) begin(log *slog.Logger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		return
	}
	p.log, p.stop = log, make(chan struct{})
	go p.watch(p.stop)
}

func (p *startupProgress) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		close(p.stop)
		p.stop = nil
	}
	p.name = ""
}

// step records that startup has moved on to name.
func (p *startupProgress) step(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.name, p.since, p.warned = name, time.Now(), false
}

func (p *startupProgress) watch(stop chan struct{}) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			p.mu.Lock()
			name, d, log := p.name, time.Since(p.since), p.log
			report := name != "" && !p.warned && d > slowStartupStep
			if report {
				p.warned = true
			}
			p.mu.Unlock()
			if report && log != nil {
				log.Warn("startup step is taking long", "step", name, "for", d.Round(100*time.Millisecond))
			}
		}
	}
}
