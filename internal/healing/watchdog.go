package healing

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// WatchdogConfig configures the process watchdog. The watchdog runs as a
// SEPARATE process (vortex watchdog) watching the main vortex via its pidfile;
// it restarts vortex if the process disappears.
type WatchdogConfig struct {
	PIDFile         string        // vortex pidfile path
	BinaryPath      string        // path to the vortex binary
	ConfigPath      string        // vortex.cue path
	HealthURL       string        // readiness probe (default http://localhost:9090/health)
	RestartDelay    time.Duration // wait before restart (default 5s)
	MaxRestarts     int           // max restarts per hour (default 10)
	CheckInterval   time.Duration // process check cadence (default 10s)
	NotifyOnRestart bool          // send an alert on restart

	// Injectable hooks (tests provide stubs; production leaves nil).
	isAlive func(pidfile string) bool                  // override process liveness
	spawn   func(ctx context.Context) error            // override binary launch
	waitUp  func(ctx context.Context, url string) bool // override readiness wait
	notify  Notifier                                   // optional restart alerts
	log     *slog.Logger
}

// Watchdog supervises the main vortex process.
type Watchdog struct {
	cfg WatchdogConfig

	mu           sync.Mutex
	restarts     []time.Time // timestamps within the last hour
	restartCount int
	lastRestart  time.Time
}

// NewWatchdog constructs a watchdog with defaults applied.
func NewWatchdog(cfg WatchdogConfig) *Watchdog {
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = 5 * time.Second
	}
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = 10
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 10 * time.Second
	}
	if cfg.HealthURL == "" {
		cfg.HealthURL = "http://localhost:9090/health"
	}
	if cfg.log == nil {
		cfg.log = slog.Default()
	}
	if cfg.isAlive == nil {
		cfg.isAlive = PidfileAlive
	}
	w := &Watchdog{cfg: cfg}
	if cfg.spawn == nil {
		w.cfg.spawn = w.launchVortex
	}
	if cfg.waitUp == nil {
		w.cfg.waitUp = waitForHealth
	}
	return w
}

// Watch supervises vortex until ctx is cancelled. It checks process liveness on
// CheckInterval; when the process is gone it waits RestartDelay then restarts,
// bounded by MaxRestarts per rolling hour.
func (w *Watchdog) Watch(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if w.cfg.isAlive(w.cfg.PIDFile) {
				continue
			}
			w.cfg.log.Warn("watchdog: vortex process not running", "pidfile", w.cfg.PIDFile)
			if !w.allowRestart() {
				w.cfg.log.Error("watchdog: max restarts exceeded, giving up", "max", w.cfg.MaxRestarts)
				w.notify(ctx, "critical", "VORTEX Watchdog",
					fmt.Sprintf("⛔ vortex down and max restarts (%d/hr) exceeded — not restarting.", w.cfg.MaxRestarts))
				continue
			}
			w.restart(ctx)
		}
	}
}

// allowRestart reports whether another restart is permitted this rolling hour,
// pruning timestamps older than an hour.
func (w *Watchdog) allowRestart() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := time.Now().Add(-time.Hour)
	kept := w.restarts[:0]
	for _, t := range w.restarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	w.restarts = kept
	return len(w.restarts) < w.cfg.MaxRestarts
}

// restart waits RestartDelay, launches vortex, and waits for readiness.
func (w *Watchdog) restart(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(w.cfg.RestartDelay):
	}

	w.cfg.log.Info("watchdog restarting vortex", "binary", w.cfg.BinaryPath, "config", w.cfg.ConfigPath)
	w.mu.Lock()
	w.restarts = append(w.restarts, time.Now())
	w.restartCount++
	w.lastRestart = time.Now()
	w.mu.Unlock()

	if err := w.cfg.spawn(ctx); err != nil {
		w.cfg.log.Error("watchdog: restart failed to launch", "err", err)
		w.notify(ctx, "critical", "VORTEX Watchdog", "❌ vortex restart failed to launch: "+err.Error())
		return
	}
	if w.cfg.waitUp(ctx, w.cfg.HealthURL) {
		w.cfg.log.Info("watchdog: vortex restarted and healthy")
		w.notify(ctx, "info", "VORTEX Watchdog", "🔄 vortex was down and has been restarted (now healthy).")
	} else {
		w.cfg.log.Error("watchdog: vortex restarted but did not become healthy")
		w.notify(ctx, "critical", "VORTEX Watchdog", "⚠️ vortex restarted but /health did not return 200 within 30s.")
	}
}

// launchVortex starts `vortex start --config <cfg>` detached.
func (w *Watchdog) launchVortex(_ context.Context) error {
	cmd := exec.Command(w.cfg.BinaryPath, "start", "--config", w.cfg.ConfigPath) //nolint:gosec // configured paths
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// notify sends an alert when configured and NotifyOnRestart is set.
func (w *Watchdog) notify(ctx context.Context, severity, title, body string) {
	if w.cfg.notify == nil || !w.cfg.NotifyOnRestart {
		return
	}
	_ = w.cfg.notify.Send(ctx, severity, title, body)
}

// RestartCount returns the total restarts attempted.
func (w *Watchdog) RestartCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.restartCount
}

// LastRestart returns the time of the most recent restart (zero if none).
func (w *Watchdog) LastRestart() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastRestart
}

// PidfileAlive reports whether the pidfile names a running process (exported
// for the watchdog status command).
func PidfileAlive(pidfile string) bool {
	data, err := os.ReadFile(pidfile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(bytesTrim(data)))
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return signalZero(proc) == nil
}

// waitForHealth polls url until it returns 200 or 30s elapses.
func waitForHealth(ctx context.Context, url string) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			if resp, derr := http.DefaultClient.Do(req); derr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return true
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
