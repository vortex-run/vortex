// Package api hosts VORTEX's management HTTP server. In M1.2 this is just a
// health endpoint that reports liveness and the active config hash, so a reload
// can be verified externally (e.g. curl localhost:9090/health). It is built on
// net/http from the standard library (Non-Negotiable Rule #10).
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/vortex-run/vortex/internal/config"
)

// DefaultAddr is the management server's default listen address.
const DefaultAddr = ":9090"

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
	mux.HandleFunc("POST /internal/reload", s.handleInternalReload)
	mux.HandleFunc("POST /internal/shutdown", s.handleInternalShutdown)
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
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
