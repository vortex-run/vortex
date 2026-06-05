package proxyhttp

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Server defaults.
const (
	defaultReadTimeout  = 30 * time.Second
	defaultWriteTimeout = 30 * time.Second
	defaultIdleTimeout  = 90 * time.Second
	serverDrainTimeout  = 30 * time.Second
)

// ServerConfig configures the internet-facing HTTP/HTTPS server.
type ServerConfig struct {
	Addr      string
	TLSConfig *tls.Config // nil = plain HTTP
	Router    *Router
	// PolicyMiddleware, when non-nil, wraps the router so every request is
	// evaluated against the authorization policy before reaching a route. A nil
	// value (the default) means no policy enforcement.
	PolicyMiddleware func(http.Handler) http.Handler
	ReadTimeout      time.Duration // default 30s
	WriteTimeout     time.Duration // default 30s
	IdleTimeout      time.Duration // default 90s
}

// ServerStats is a snapshot of server-level counters.
type ServerStats struct {
	ActiveConns int64
	TotalReqs   int64
	ErrorRate   float64 // errors / total (cumulative; windowed in M5)
}

// Server wraps a net/http server with graceful shutdown and connection stats.
type Server struct {
	srv  *http.Server
	addr atomic.Pointer[string]

	activeConns atomic.Int64
	totalReqs   atomic.Int64
	errorReqs   atomic.Int64
}

// NewServer builds a Server from cfg (zero timeouts take defaults).
func NewServer(cfg ServerConfig) *Server {
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}

	// Apply policy enforcement (if configured) closest to the router, then
	// instrument the whole chain for connection/error stats.
	var handler http.Handler = cfg.Router
	if cfg.PolicyMiddleware != nil {
		handler = cfg.PolicyMiddleware(handler)
	}

	s := &Server{}
	s.srv = &http.Server{
		Addr:         cfg.Addr,
		Handler:      s.instrument(handler),
		TLSConfig:    cfg.TLSConfig,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
		ConnState:    s.onConnState,
	}
	return s
}

// instrument wraps the router to count total and error responses.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.totalReqs.Add(1)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if sw.status >= 500 {
			s.errorReqs.Add(1)
		}
	})
}

// onConnState tracks active connections via the http.Server ConnState hook.
func (s *Server) onConnState(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		s.activeConns.Add(1)
	case http.StateClosed, http.StateHijacked:
		s.activeConns.Add(-1)
	}
}

// ListenAndServe binds and serves until ctx is cancelled, then gracefully
// shuts down (bounded by serverDrainTimeout). It returns nil on clean shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	s.addr.Store(&addr)

	errCh := make(chan error, 1)
	go func() {
		if s.srv.TLSConfig != nil {
			// Certs come from TLSConfig (GetCertificate / Certificates).
			errCh <- s.srv.ServeTLS(ln, "", "")
		} else {
			errCh <- s.srv.Serve(ln)
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), serverDrainTimeout)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// Addr returns the actual bound address (useful when Addr was ":0").
func (s *Server) Addr() string {
	if p := s.addr.Load(); p != nil {
		return *p
	}
	return s.srv.Addr
}

// Stats returns a snapshot of server counters.
func (s *Server) Stats() ServerStats {
	total := s.totalReqs.Load()
	var rate float64
	if total > 0 {
		rate = float64(s.errorReqs.Load()) / float64(total)
	}
	return ServerStats{
		ActiveConns: s.activeConns.Load(),
		TotalReqs:   total,
		ErrorRate:   rate,
	}
}

// statusWriter records the response status code for error-rate accounting.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// Hijack forwards to the underlying ResponseWriter's Hijacker so WebSocket
// upgrades (which hijack the connection) work through the instrumented server.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

// Flush forwards to the underlying Flusher so streaming responses still flush.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
