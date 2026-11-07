package cmd

import (
	"testing"

	"github.com/sandeepbollavaram/vortex/internal/config"
)

// Audit F-16 / NV-04: the TLS manager's host allow-list comes from the TLS
// routes, and is unrestricted (nil) as soon as one TLS route has no host.
func TestTLSRouteHosts(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{
		{Name: "a", Protocol: "https", Host: "app.example.com"},
		{Name: "b", Protocol: "h3", Host: "*.svc.example.com"},
		{Name: "c", Protocol: "http"}, // plain http: no certificate, ignored
	}}
	got := tlsRouteHosts(cfg, quietLogger())
	if len(got) != 2 || got[0] != "app.example.com" || got[1] != "*.svc.example.com" {
		t.Fatalf("hosts = %v", got)
	}
	cfg.Routes = append(cfg.Routes, config.Route{Name: "d", Protocol: "https"})
	if got := tlsRouteHosts(cfg, quietLogger()); got != nil {
		t.Errorf("a host-less TLS route must make the list unrestricted, got %v", got)
	}
}
