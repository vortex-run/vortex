// Package api hosts VORTEX's management HTTP server. In M1.2 this is just a
// health endpoint that reports liveness and the active config hash, so a reload
// can be verified externally (e.g. curl localhost:9090/health). It is built on
// net/http from the standard library (Non-Negotiable Rule #10).
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/pkg/logger"
)

// DefaultAddr is the management server's default listen address.
const DefaultAddr = ":9090"

// correlationHeader is the request/response header carrying the correlation ID
// that ties together all log lines emitted while handling one request.
const correlationHeader = "X-Correlation-ID"

// Server is the management HTTP server.
type Server struct {
	srv       *http.Server
	holder    *config.Holder
	log       *slog.Logger
	version   string
	startTime time.Time

	// reloadFunc re-reads and re-validates config; set via SetReloadFunc.
	reloadFunc func() error
	// shutdownFunc triggers a graceful shutdown; set via SetShutdownFunc.
	shutdownFunc func()
}

// SetReloadFunc registers the callback invoked by POST /internal/reload. It
// should re-read and re-validate the config and return an error if invalid.
func (s *Server) SetReloadFunc(fn func() error) { s.reloadFunc = fn }

// SetShutdownFunc registers the callback invoked by POST /internal/shutdown to
// begin a graceful shutdown.
func (s *Server) SetShutdownFunc(fn func()) { s.shutdownFunc = fn }

// New constructs a management Server. holder supplies the live config so
// /health always reports the currently active hash, including after a reload.
func New(addr string, holder *config.Holder, version string, log *slog.Logger) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	s := &Server{
		holder:    holder,
		log:       log,
		version:   version,
		startTime: time.Now(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.HandleFunc("POST /internal/reload", s.handleInternalReload)
	mux.HandleFunc("POST /internal/shutdown", s.handleInternalShutdown)
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.correlationMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// correlationMiddleware ensures every request carries a correlation ID. It
// reuses an incoming X-Correlation-ID header when present (so a caller's ID
// flows through the system) or generates a fresh 32-char hex ID otherwise. The
// ID is echoed back in the response header — set before the wrapped handler can
// write anything — and stored in the request context so downstream logging
// (via pkg/logger) automatically tags log lines with it.
func (s *Server) correlationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(correlationHeader)
		if id == "" {
			id = newCorrelationID()
		}
		// Must be set before the handler writes the body or status.
		w.Header().Set(correlationHeader, id)
		ctx := logger.WithCorrelationID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newCorrelationID returns a 32-character hex string (16 random bytes). If the
// system RNG fails (effectively never), it falls back to a timestamp-derived
// value so a request is never left without an ID.
func newCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000")))[:32]
	}
	return hex.EncodeToString(b[:])
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.srv.Addr }

// healthResponse is the JSON body returned by /health.
type healthResponse struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	ConfigHash  string `json:"config_hash"`
	ClusterName string `json:"cluster_name"`
	Uptime      string `json:"uptime"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	cfg := s.holder.Get()
	resp := healthResponse{
		Status:      "ok",
		Version:     s.version,
		ConfigHash:  cfg.Hash(),
		ClusterName: cfg.Cluster.Name,
		Uptime:      time.Since(s.startTime).Round(time.Second).String(),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.Error("encoding health response", "err", err)
	}
}

// handleReady is a readiness probe. The management server only begins serving
// once boot has completed, so reaching this handler means VORTEX is ready; it
// returns 200 with a small JSON body. (As subsystems gain their own readiness
// gates in later milestones, this will aggregate them.)
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]bool{"ready": true}); err != nil {
		s.log.Error("encoding ready response", "err", err)
	}
}

// Start begins serving in a background goroutine. It returns immediately; serve
// errors other than graceful shutdown are logged.
func (s *Server) Start() {
	go func() {
		s.log.Info("management API listening", "addr", s.srv.Addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("management API stopped unexpectedly", "err", err)
		}
	}()
}

// Shutdown gracefully stops the server, respecting ctx's deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
