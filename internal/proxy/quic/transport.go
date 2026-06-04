// Package proxyquic implements VORTEX's QUIC / HTTP/3 transport (build plan
// M2.3) on top of github.com/quic-go/quic-go. It serves the same proxyhttp
// Router over HTTP/3 and provides a dual-stack listener that runs QUIC (UDP)
// alongside the TCP HTTP/1.1+HTTP/2 server, degrading to TCP-only when QUIC
// cannot bind.
package proxyquic

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"crypto/tls"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
)

// QUIC transport defaults.
const (
	defaultMaxStreams  = 100
	defaultIdleTimeout = 30 * time.Second
)

// QUICConfig configures a QUIC/HTTP3 Transport.
type QUICConfig struct {
	// Addr is the UDP listen address, e.g. ":443". Required.
	Addr string
	// TLSConfig is required — QUIC mandates TLS 1.3.
	TLSConfig *tls.Config
	// Router handles requests received over HTTP/3. Required.
	Router *proxyhttp.Router
	// MaxStreams is the max concurrent bidirectional streams per connection.
	// Default 100.
	MaxStreams int64
	// IdleTimeout is the max idle duration before a connection is closed.
	// Default 30s. KeepAlive is set to half this.
	IdleTimeout time.Duration
	// Enable0RTT allows 0-RTT session resumption. Default true.
	Enable0RTT bool
}

// QUICStats is a snapshot of QUIC transport counters.
type QUICStats struct {
	ActiveConns     int64
	TotalConns      int64
	ZeroRTTAccepted int64 // stub — incremented when 0-RTT accounting is wired
}

// Transport serves the configured Router over HTTP/3.
type Transport struct {
	cfg    QUICConfig
	server *http3.Server

	activeConns atomic.Int64
	totalConns  atomic.Int64
	zeroRTT     atomic.Int64
}

// NewTransport validates cfg and constructs a Transport.
func NewTransport(cfg QUICConfig) (*Transport, error) {
	if cfg.TLSConfig == nil {
		return nil, errors.New("quic transport: TLSConfig is required (QUIC mandates TLS)")
	}
	if cfg.Router == nil {
		return nil, errors.New("quic transport: Router is required")
	}
	if cfg.Addr == "" {
		return nil, errors.New("quic transport: Addr is required")
	}
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = defaultMaxStreams
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}

	t := &Transport{cfg: cfg}
	t.server = &http3.Server{
		Addr:      cfg.Addr,
		TLSConfig: cfg.TLSConfig,
		Handler:   cfg.Router,
		QUICConfig: &quic.Config{
			MaxIncomingStreams: cfg.MaxStreams,
			MaxIdleTimeout:     cfg.IdleTimeout,
			KeepAlivePeriod:    cfg.IdleTimeout / 2,
			Allow0RTT:          cfg.Enable0RTT,
		},
	}
	return t, nil
}

// ListenAndServe binds the UDP socket and serves HTTP/3 until ctx is cancelled,
// then closes the server. It returns nil on clean shutdown and an error only on
// an unexpected bind/serve failure.
func (t *Transport) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		// ListenAndServe uses the server's Addr + TLSConfig (in-memory certs).
		errCh <- t.server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		_ = t.server.Close()
		// Drain the serve goroutine's result.
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, quic.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Server returns the underlying http3.Server (used by the dual-stack listener
// to set Alt-Svc headers on TCP responses).
func (t *Transport) Server() *http3.Server { return t.server }

// Stats returns a snapshot of QUIC transport counters.
func (t *Transport) Stats() QUICStats {
	return QUICStats{
		ActiveConns:     t.activeConns.Load(),
		TotalConns:      t.totalConns.Load(),
		ZeroRTTAccepted: t.zeroRTT.Load(),
	}
}
