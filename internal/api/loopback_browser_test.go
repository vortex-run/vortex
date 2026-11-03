package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLoopbackTrust_BrowserRequestsNeedAKey: a browser on this machine
// connects from loopback for any website, so cross-site and DNS-rebinding
// requests don't get the loopback bypass, while the CLI and a same-origin
// dashboard still do.
func TestLoopbackTrust_BrowserRequestsNeedAKey(t *testing.T) {
	t.Setenv("VORTEX_TRUST_LOOPBACK", "")
	s, _ := authedServer(t)
	shutdowns := make(chan struct{}, 8)
	s.SetShutdownFunc(func() { shutdowns <- struct{}{} })

	cases := []struct {
		name    string
		method  string
		url     string
		headers map[string]string
		trusted bool
	}{
		{"CLI stop", http.MethodPost, "http://127.0.0.1:9090/internal/shutdown", nil, true},
		{"CLI via localhost", http.MethodGet, "http://localhost:9090/api/audit", nil, true},
		{"CLI via ::1", http.MethodGet, "http://[::1]:9090/api/audit", nil, true},
		{"dashboard same-origin", http.MethodGet, "http://localhost:9090/api/audit",
			map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"dashboard same-origin POST", http.MethodPost, "http://127.0.0.1:9090/api/audit/verify",
			map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:9090"}, true},

		{"DNS rebinding read", http.MethodGet, "http://rebind.attacker.example:9090/api/audit", nil, false},
		{"DNS rebinding shutdown", http.MethodPost, "http://rebind.attacker.example:9090/internal/shutdown", nil, false},
		{"cross-site form POST", http.MethodPost, "http://127.0.0.1:9090/internal/shutdown",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		{"cross-site POST, old browser", http.MethodPost, "http://127.0.0.1:9090/internal/shutdown",
			map[string]string{"Origin": "https://evil.example"}, false},
		{"other local port", http.MethodPost, "http://127.0.0.1:9090/internal/shutdown",
			map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://127.0.0.1:3000"}, false},
		{"sandboxed iframe", http.MethodPost, "http://127.0.0.1:9090/internal/shutdown",
			map[string]string{"Origin": "null"}, false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.url, nil)
		req.RemoteAddr = "127.0.0.1:5555"
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rec := serve(s, req)
		if c.trusted && rec.Code == http.StatusUnauthorized {
			t.Errorf("%s: %s %s = 401, want the loopback bypass", c.name, c.method, c.url)
		}
		if !c.trusted && rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %s %s = %d, want 401 (no bypass without a key)", c.name, c.method, c.url, rec.Code)
		}
	}

	// Exactly one shutdown went through: the CLI's.
	deadline := time.After(time.Second)
	got := 0
	for done := false; !done; {
		select {
		case <-shutdowns:
			got++
		case <-deadline:
			done = true
		}
	}
	if got != 1 {
		t.Errorf("shutdown handler ran %d times, want 1 (CLI only)", got)
	}
}

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:9090": true, "127.0.0.1": true, "localhost:9090": true, "LOCALHOST": true,
		"[::1]:9090": true, "127.5.5.5:1": true,
		"example.com": false, "rebind.attacker.example:9090": false, "10.0.0.5:9090": false,
		"localhost.attacker.example": false, "": false,
	} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}
