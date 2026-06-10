package proxyhttp

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// applyHeaders runs a request through the security headers middleware wrapping
// handler and returns the recorded response. When secure is true the request
// carries a TLS connection state (as an HTTPS request would).
func applyHeaders(t *testing.T, cfg SecurityHeadersConfig, secure bool, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if secure {
		req.TLS = &tls.ConnectionState{}
	}
	rec := httptest.NewRecorder()
	NewSecurityHeaders(cfg)(handler).ServeHTTP(rec, req)
	return rec
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func TestSecurityHeaders_HSTSOnHTTPS(t *testing.T) {
	rec := applyHeaders(t, SecurityHeadersConfig{}, true, okHandler)
	want := "max-age=31536000; includeSubDomains; preload"
	if got := rec.Header().Get("Strict-Transport-Security"); got != want {
		t.Errorf("HSTS = %q, want %q", got, want)
	}
}

func TestSecurityHeaders_NoHSTSOnPlainHTTP(t *testing.T) {
	rec := applyHeaders(t, SecurityHeadersConfig{}, false, okHandler)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should not be set on plain HTTP, got %q", got)
	}
}

func TestSecurityHeaders_StandardHeadersPresent(t *testing.T) {
	rec := applyHeaders(t, SecurityHeadersConfig{}, false, okHandler)
	cases := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"X-XSS-Protection":        "1; mode=block",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "geolocation=(), camera=(), microphone=()",
		"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'",
	}
	for key, want := range cases {
		if got := rec.Header().Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestSecurityHeaders_ServerHeaderRemoved(t *testing.T) {
	rec := applyHeaders(t, SecurityHeadersConfig{}, false, func(w http.ResponseWriter, _ *http.Request) {
		// Simulate an upstream response that identifies its server stack.
		w.Header().Set("Server", "nginx/1.24")
		w.Header().Set("X-Powered-By", "PHP/8.3")
		w.WriteHeader(http.StatusOK)
	})
	if got := rec.Header().Get("Server"); got != "" {
		t.Errorf("Server header should be removed, got %q", got)
	}
	if got := rec.Header().Get("X-Powered-By"); got != "" {
		t.Errorf("X-Powered-By header should be removed, got %q", got)
	}
}

func TestSecurityHeaders_UpstreamCSPPreserved(t *testing.T) {
	rec := applyHeaders(t, SecurityHeadersConfig{}, false, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.WriteHeader(http.StatusOK)
	})
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'" {
		t.Errorf("upstream CSP should be preserved, got %q", got)
	}
}

func TestSecurityHeaders_VersionHeaderOptIn(t *testing.T) {
	rec := applyHeaders(t, SecurityHeadersConfig{}, false, okHandler)
	if got := rec.Header().Get("X-Vortex-Version"); got != "" {
		t.Errorf("X-Vortex-Version should be absent by default, got %q", got)
	}

	rec = applyHeaders(t, SecurityHeadersConfig{Version: "v1.2.3"}, false, okHandler)
	if got := rec.Header().Get("X-Vortex-Version"); got != "v1.2.3" {
		t.Errorf("X-Vortex-Version = %q, want v1.2.3", got)
	}
}

func TestSecurityHeaders_AppliedOnImplicitWrite(t *testing.T) {
	// Handler writes the body without calling WriteHeader first; headers must
	// still be applied before the implicit 200.
	rec := applyHeaders(t, SecurityHeadersConfig{}, false, okHandler)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("headers missing on implicit-write path: nosniff = %q", got)
	}
}

func TestSecurityHeaders_ServerWiresMiddleware(t *testing.T) {
	// End-to-end through NewServer: every response from the proxy server chain
	// carries the security headers.
	router := NewRouter()
	router.Handle("/*", http.HandlerFunc(okHandler))
	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Router: router})

	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("proxy server response missing security headers: X-Frame-Options = %q", got)
	}
	if got := rec.Header().Get("Server"); got != "" {
		t.Errorf("Server header should be removed, got %q", got)
	}
}
