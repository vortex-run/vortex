package tenancy

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// enforcerWith builds an enforcer whose registry has one namespace with the
// given connection limit.
func enforcerWith(t *testing.T, maxConns int64) (*Enforcer, string) {
	t.Helper()
	reg := NewRegistry()
	_, err := reg.Create(NamespaceConfig{
		ID: "ns-1", OrgID: "org-a", Quotas: QuotaConfig{MaxConnections: maxConns},
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewEnforcer(reg), "ns-1"
}

func TestEnforcer_HTTPAllowsUnderQuota(t *testing.T) {
	e, ns := enforcerWith(t, 5)
	h := e.HTTPMiddleware(ns)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("under quota status = %d, want 200", rec.Code)
	}
}

func TestEnforcer_HTTP429AtLimit(t *testing.T) {
	e, ns := enforcerWith(t, 1)
	// Hold one connection open inside the handler while a second request races
	// in, so the active count reaches the limit.
	block := make(chan struct{})
	started := make(chan struct{})
	h := e.HTTPMiddleware(ns)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-block
		w.WriteHeader(http.StatusOK)
	}))

	go func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-started // first request is now in-flight, active=1

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("at-limit status = %d, want 429", rec.Code)
	}
	close(block)
}

func TestEnforcer_TCPAllowsUnderLimit(t *testing.T) {
	e, ns := enforcerWith(t, 2)
	wrap := e.TCPMiddleware(ns)
	c1, c2 := net.Pipe()
	defer func() { _ = c2.Close() }()
	wrapped, err := wrap(c1)
	if err != nil {
		t.Fatalf("first conn should be allowed: %v", err)
	}
	if wrapped == nil {
		t.Fatal("wrapped conn should not be nil")
	}
}

func TestEnforcer_TCPErrorAtLimit(t *testing.T) {
	e, ns := enforcerWith(t, 1)
	wrap := e.TCPMiddleware(ns)
	a1, _ := net.Pipe()
	if _, err := wrap(a1); err != nil {
		t.Fatalf("first conn allowed: %v", err)
	}
	b1, _ := net.Pipe()
	if _, err := wrap(b1); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("second conn = %v, want ErrQuotaExceeded", err)
	}
}

func TestEnforcer_RecordBandwidth(t *testing.T) {
	e, ns := enforcerWith(t, 0)
	e.RecordBandwidth(ns, 500)
	e.RecordBandwidth(ns, 250)
	if got := e.Stats(ns).BandwidthUsed; got != 750 {
		t.Errorf("BandwidthUsed = %d, want 750", got)
	}
}

func TestEnforcer_StatsAccurate(t *testing.T) {
	e, ns := enforcerWith(t, 10)
	e.SetRouteCount(ns, 3)
	wrap := e.TCPMiddleware(ns)
	a1, _ := net.Pipe()
	_, _ = wrap(a1)
	s := e.Stats(ns)
	if s.ActiveConns != 1 {
		t.Errorf("ActiveConns = %d, want 1", s.ActiveConns)
	}
	if s.RouteCount != 3 {
		t.Errorf("RouteCount = %d, want 3", s.RouteCount)
	}
}

func TestEnforcer_CounterDecrementsAfterRequest(t *testing.T) {
	e, ns := enforcerWith(t, 5)
	h := e.HTTPMiddleware(ns)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	// After the request completes, the active counter must be back to 0.
	if got := e.Stats(ns).ActiveConns; got != 0 {
		t.Errorf("ActiveConns after request = %d, want 0", got)
	}
}

func TestEnforcer_UnknownNamespaceFailsOpen(t *testing.T) {
	e := NewEnforcer(NewRegistry())
	h := e.HTTPMiddleware("missing")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("unknown namespace should fail open, got %d", rec.Code)
	}
}
