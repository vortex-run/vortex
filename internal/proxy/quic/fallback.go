package proxyquic

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
)

// DualStackConfig configures a server that listens on both QUIC (UDP) and TCP.
type DualStackConfig struct {
	// Addr is the shared listen address, e.g. ":443".
	Addr string
	// TLSConfig is used by both the TCP and QUIC listeners.
	TLSConfig *tls.Config
	// Router handles requests on both transports.
	Router *proxyhttp.Router
	// QUICConfig customizes the QUIC transport. Addr/TLSConfig/Router are
	// filled from the DualStack fields if unset.
	QUICConfig QUICConfig
	// Logger receives degradation warnings; defaults to slog.Default.
	Logger *slog.Logger
}

// DualStackStats aggregates QUIC and TCP statistics.
type DualStackStats struct {
	QUIC          QUICStats
	TCP           proxyhttp.ServerStats
	QUICAvailable bool
}

// DualStack runs a QUIC/HTTP3 transport alongside a TCP HTTP/1.1+HTTP/2 server,
// sharing one Router. TCP is required; if QUIC cannot bind, the server degrades
// to TCP-only. TCP responses carry an Alt-Svc header advertising HTTP/3 so
// capable clients upgrade on their next connection.
type DualStack struct {
	cfg       DualStackConfig
	log       *slog.Logger
	quic      *Transport
	tcp       *proxyhttp.Server
	quicAvail atomic.Bool
}

// NewDualStack validates cfg and constructs a DualStack.
func NewDualStack(cfg DualStackConfig) (*DualStack, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// Fill QUIC sub-config from the shared fields where unset.
	qcfg := cfg.QUICConfig
	if qcfg.Addr == "" {
		qcfg.Addr = cfg.Addr
	}
	if qcfg.TLSConfig == nil {
		qcfg.TLSConfig = cfg.TLSConfig
	}
	if qcfg.Router == nil {
		qcfg.Router = cfg.Router
	}

	quicTransport, err := NewTransport(qcfg)
	if err != nil {
		return nil, err
	}

	d := &DualStack{cfg: cfg, log: cfg.Logger, quic: quicTransport}

	// The TCP server wraps the router to add Alt-Svc on every response.
	tcpHandler := d.withAltSvc(cfg.Router)
	tcpRouter := proxyhttp.NewRouter()
	tcpRouter.Handle("/*", tcpHandler) // catch-all; per-route matching done by inner router
	d.tcp = proxyhttp.NewServer(proxyhttp.ServerConfig{
		Addr:      cfg.Addr,
		TLSConfig: cfg.TLSConfig,
		Router:    tcpRouter,
	})
	return d, nil
}

// withAltSvc wraps next so every TCP response advertises HTTP/3 via Alt-Svc.
func (d *DualStack) withAltSvc(next http.Handler) http.Handler {
	altSvc := `h3=":443"; ma=86400`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", altSvc)
		next.ServeHTTP(w, r)
	})
}

// ListenAndServe starts both listeners. TCP is required: if it fails to bind,
// ListenAndServe returns its error. QUIC is best-effort: a QUIC bind failure is
// logged and the server continues TCP-only. It returns nil once both (or just
// TCP) have stopped after ctx cancellation.
func (d *DualStack) ListenAndServe(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// TCP (required).
	tcpErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tcpErr <- d.tcp.ListenAndServe(ctx)
	}()

	// QUIC (best-effort). Optimistically mark available before starting; the
	// goroutine flips it off if the UDP bind/serve fails (e.g. port in use),
	// degrading to TCP-only. Set true before spawning so the goroutine's false
	// always wins on failure.
	d.quicAvail.Store(true)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.quic.ListenAndServe(ctx); err != nil {
			d.quicAvail.Store(false)
			d.log.Warn("QUIC unavailable, serving TCP only", "err", err)
		}
	}()

	// Wait for a TCP bind/serve error (fatal) or ctx cancellation.
	select {
	case err := <-tcpErr:
		if err != nil {
			cancel()
			wg.Wait()
			return err
		}
		// TCP returned nil (clean shutdown after cancel).
	case <-ctx.Done():
	}

	cancel()
	wg.Wait()
	return nil
}

// Stats returns aggregated QUIC + TCP statistics.
func (d *DualStack) Stats() DualStackStats {
	return DualStackStats{
		QUIC:          d.quic.Stats(),
		TCP:           d.tcp.Stats(),
		QUICAvailable: d.quicAvail.Load(),
	}
}

// Addr returns the bound TCP address (useful when the configured port was 0).
func (d *DualStack) Addr() string { return d.tcp.Addr() }
