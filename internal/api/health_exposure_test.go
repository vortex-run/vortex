package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealth_RemoteCallersDontSeeRoutes: /health is public; route names,
// listen addresses and connection counts are shown only to trusted local
// callers (the CLI, the local dashboard), like the cluster name.
func TestHealth_RemoteCallersDontSeeRoutes(t *testing.T) {
	t.Setenv("VORTEX_TRUST_LOOPBACK", "")
	s, _ := authedServer(t)
	s.SetRouteStats(func() []RouteHealth {
		return []RouteHealth{{Name: "billing-db", Protocol: "tcp", Listen: ":5432", Active: 7}}
	})

	get := func(url, remote string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.RemoteAddr = remote
		rec := serve(s, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s from %s = %d", url, remote, rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	remote := get("http://vortex.example/health", "203.0.113.9:4444")
	if _, ok := remote["routes"]; ok {
		t.Errorf("remote /health exposes routes: %v", remote["routes"])
	}
	if remote["status"] != "ok" || remote["version"] == nil {
		t.Errorf("remote /health lost status/version: %v", remote)
	}

	local := get("http://127.0.0.1:9090/health", "127.0.0.1:5555")
	if routes, _ := local["routes"].([]any); len(routes) != 1 {
		t.Errorf("local /health routes = %v, want the route", local["routes"])
	}
}
