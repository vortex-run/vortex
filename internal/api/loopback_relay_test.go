package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Audit F-06: a request relayed by a same-host proxy (Gubern's own edge adds
// these headers) arrives from loopback with a client-chosen Host; it must not
// get the unauthenticated loopback bypass. A genuine on-box CLI call still does.
func TestLoopbackTrusted_RefusesRelayedRequests(t *testing.T) {
	local := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r.Host = "localhost"
		return r
	}
	if !loopbackTrusted(local()) {
		t.Fatal("plain on-box CLI request must stay trusted")
	}
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-IP", "X-Vortex-Client-IP"} {
		r := local()
		r.Header.Set(h, "203.0.113.9")
		if loopbackTrusted(r) {
			t.Errorf("request carrying %s was trusted as on-box", h)
		}
	}
}
