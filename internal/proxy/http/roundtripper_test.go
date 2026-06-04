package proxyhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// captureBackend starts a test backend that records the last request headers it
// received and returns a fixed body. The returned func returns those headers.
func captureBackend(t *testing.T, body string) (*httptest.Server, func() http.Header) {
	t.Helper()
	var last http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = r.Header.Clone()
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header { return last }
}

// forward sends req through a PooledRoundTripper targeting srv, fully reads and
// closes the response body, and returns the status code and body. The request
// URL is rewritten to the backend, as the handler would do.
func forward(t *testing.T, srv *httptest.Server, req *http.Request) (int, string) {
	t.Helper()
	rt := NewRoundTripper(RoundTripperConfig{})
	u, _ := url.Parse(srv.URL)
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	req.RequestURI = ""
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func newReq(t *testing.T, host string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+host+"/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.RemoteAddr = "203.0.113.7:54321"
	return req
}

func TestRT_StripsHopByHopHeaders(t *testing.T) {
	srv, got := captureBackend(t, "ok")
	req := newReq(t, "app.example.com")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("TE", "trailers")
	_, _ = forward(t, srv, req)

	h := got()
	for _, name := range []string{"Keep-Alive", "Te"} {
		if h.Get(name) != "" {
			t.Errorf("hop-by-hop header %q should be stripped, got %q", name, h.Get(name))
		}
	}
}

func TestRT_StripsHeadersNamedInConnection(t *testing.T) {
	srv, got := captureBackend(t, "ok")
	req := newReq(t, "app.example.com")
	req.Header.Set("Connection", "close, X-Custom")
	req.Header.Set("X-Custom", "secret")
	_, _ = forward(t, srv, req)

	if v := got().Get("X-Custom"); v != "" {
		t.Errorf("X-Custom named in Connection should be stripped, got %q", v)
	}
}

func TestRT_SetsXForwardedFor(t *testing.T) {
	srv, got := captureBackend(t, "ok")
	_, _ = forward(t, srv, newReq(t, "app.example.com"))
	if v := got().Get("X-Forwarded-For"); v != "203.0.113.7" {
		t.Errorf("X-Forwarded-For = %q, want 203.0.113.7", v)
	}
}

func TestRT_AppendsXForwardedFor(t *testing.T) {
	srv, got := captureBackend(t, "ok")
	req := newReq(t, "app.example.com")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	_, _ = forward(t, srv, req)
	if v := got().Get("X-Forwarded-For"); v != "1.2.3.4, 203.0.113.7" {
		t.Errorf("X-Forwarded-For = %q, want \"1.2.3.4, 203.0.113.7\"", v)
	}
}

func TestRT_SetsXForwardedHost(t *testing.T) {
	srv, got := captureBackend(t, "ok")
	_, _ = forward(t, srv, newReq(t, "app.example.com"))
	if v := got().Get("X-Forwarded-Host"); v != "app.example.com" {
		t.Errorf("X-Forwarded-Host = %q, want app.example.com", v)
	}
}

func TestRT_SetsXForwardedProto(t *testing.T) {
	srv, got := captureBackend(t, "ok")
	_, _ = forward(t, srv, newReq(t, "app.example.com"))
	if v := got().Get("X-Forwarded-Proto"); v != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http", v)
	}
}

func TestRT_SetsXRealIPWhenAbsent(t *testing.T) {
	srv, got := captureBackend(t, "ok")
	_, _ = forward(t, srv, newReq(t, "app.example.com"))
	if v := got().Get("X-Real-Ip"); v != "203.0.113.7" {
		t.Errorf("X-Real-IP = %q, want 203.0.113.7", v)
	}
}

func TestRT_DoesNotOverwriteXRealIP(t *testing.T) {
	srv, got := captureBackend(t, "ok")
	req := newReq(t, "app.example.com")
	req.Header.Set("X-Real-IP", "9.9.9.9")
	_, _ = forward(t, srv, req)
	if v := got().Get("X-Real-Ip"); v != "9.9.9.9" {
		t.Errorf("X-Real-IP = %q, want preserved 9.9.9.9", v)
	}
}

func TestRT_ReturnsResponse(t *testing.T) {
	srv, _ := captureBackend(t, "hello from backend")
	status, body := forward(t, srv, newReq(t, "app.example.com"))
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "hello from backend" {
		t.Errorf("body = %q, want 'hello from backend'", body)
	}
}
