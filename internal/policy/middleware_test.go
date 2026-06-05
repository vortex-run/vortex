package policy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// engineFromPolicy builds an Engine from inline Rego for middleware tests.
func engineFromPolicy(t *testing.T, rego string) *Engine {
	t.Helper()
	dir := writePolicy(t, rego)
	e, err := NewEngine(EngineConfig{PolicyDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// okHandler records whether it was reached and returns 200.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_AllowPassesThrough(t *testing.T) {
	e := engineFromPolicy(t, "package vortex\n\ndefault allow = true\n")
	var reached bool
	h := NewMiddleware(e)(okHandler(&reached))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if !reached {
		t.Error("allow-all should reach the next handler")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestMiddleware_DenyReturns403(t *testing.T) {
	e := engineFromPolicy(t, "package vortex\n\ndefault allow = false\n")
	var reached bool
	h := NewMiddleware(e)(okHandler(&reached))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if reached {
		t.Error("deny-all should NOT reach the next handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestMiddleware_DenyBodyIsJSON(t *testing.T) {
	e := engineFromPolicy(t, "package vortex\n\ndefault allow = false\n")
	h := NewMiddleware(e)(okHandler(new(bool)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/secret", nil))

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if body["error"] != "policy denied" {
		t.Errorf("error field = %q, want 'policy denied'", body["error"])
	}
	if body["path"] != "/secret" {
		t.Errorf("path field = %q, want '/secret'", body["path"])
	}
}

func TestMiddleware_EvalErrorReturns500(t *testing.T) {
	// A policy that calls to_number on a non-numeric request path raises a
	// built-in error at Eval. The engine runs with StrictBuiltinErrors, so this
	// surfaces as an evaluation error (not silent undefined), exercising the 500
	// path. input.path is "/x", which is not a valid number.
	e := engineFromPolicy(t, `package vortex

default allow = false

allow if {
	to_number(input.path) == 0
}
`)
	h := NewMiddleware(e)(okHandler(new(bool)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("500 body is not valid JSON: %v", err)
	}
	if body["error"] != "policy evaluation failed" {
		t.Errorf("error field = %q, want 'policy evaluation failed'", body["error"])
	}
}

// capturingHandler is a slog.Handler that records emitted records.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.records = append(c.records, r)
	c.mu.Unlock()
	return nil
}
func (c *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capturingHandler) WithGroup(string) slog.Handler      { return c }

func TestMiddleware_DenyLoggedAtWarn(t *testing.T) {
	capH := &capturingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capH))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := engineFromPolicy(t, "package vortex\n\ndefault allow = false\n")
	h := NewMiddleware(e)(okHandler(new(bool)))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	capH.mu.Lock()
	defer capH.mu.Unlock()
	var found bool
	for _, r := range capH.records {
		if r.Level == slog.LevelWarn && r.Message == "request denied by policy" {
			found = true
		}
	}
	if !found {
		t.Error("expected a WARN 'request denied by policy' log record")
	}
}

func TestMiddleware_RouteNameInInput(t *testing.T) {
	// Policy allows only the "api" route.
	e := engineFromPolicy(t, `package vortex

default allow = false

allow if {
	input.route == "api"
}
`)
	h := NewMiddleware(e)(okHandler(new(bool)))

	// route=admin → denied
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(SetRouteName(req.Context(), "admin"))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("route=admin status = %d, want 403", rec.Code)
	}

	// route=api → allowed
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(SetRouteName(req.Context(), "api"))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("route=api status = %d, want 200", rec.Code)
	}
}

func TestMiddleware_MethodInInput(t *testing.T) {
	// Policy denies DELETE, allows everything else.
	e := engineFromPolicy(t, `package vortex

default allow = true

allow = false if {
	input.method == "DELETE"
}
`)
	h := NewMiddleware(e)(okHandler(new(bool)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/x", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("DELETE status = %d, want 403", rec.Code)
	}
}
