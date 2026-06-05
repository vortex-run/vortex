package observability

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"
)

// defaultProfilerEndpoint is where pprof is served when none is configured.
const defaultProfilerEndpoint = "127.0.0.1:6060"

// ProfilerConfig configures the pprof profiler.
type ProfilerConfig struct {
	Enabled  bool
	Endpoint string // host:port to serve pprof; default 127.0.0.1:6060
}

// Profiler serves Go's net/http/pprof handlers on a localhost-only endpoint for
// continuous profiling. It is a no-op when disabled.
type Profiler struct {
	cfg ProfilerConfig

	mu   sync.Mutex
	srv  *http.Server
	addr string
}

// NewProfiler builds a Profiler from cfg.
func NewProfiler(cfg ProfilerConfig) *Profiler {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultProfilerEndpoint
	}
	return &Profiler{cfg: cfg}
}

// Start serves the pprof endpoints until ctx is cancelled, then shuts down
// gracefully. It is a no-op when disabled. The listener is bound to a loopback
// address only; a non-loopback Endpoint is rejected so profiles are never
// exposed off-box.
func (p *Profiler) Start(ctx context.Context) error {
	if !p.cfg.Enabled {
		return nil
	}

	host, _, err := net.SplitHostPort(p.cfg.Endpoint)
	if err != nil {
		return fmt.Errorf("observability: invalid profiler endpoint %q: %w", p.cfg.Endpoint, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("observability: profiler endpoint %q must be loopback (127.0.0.1/::1)", p.cfg.Endpoint)
	}

	ln, err := net.Listen("tcp", p.cfg.Endpoint)
	if err != nil {
		return fmt.Errorf("observability: binding profiler: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	p.mu.Lock()
	p.srv = srv
	p.addr = ln.Addr().String()
	p.mu.Unlock()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		return p.Stop()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// Addr returns the bound profiler address (useful when the port was :0).
func (p *Profiler) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addr
}

// Stop gracefully shuts the profiler server down. It is safe to call when the
// profiler was never started.
func (p *Profiler) Stop() error {
	p.mu.Lock()
	srv := p.srv
	p.mu.Unlock()
	if srv == nil {
		return nil
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("observability: shutting down profiler: %w", err)
	}
	return nil
}
