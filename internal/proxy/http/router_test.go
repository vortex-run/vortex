package proxyhttp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// markerHandler writes its id so tests can tell which route matched.
func markerHandler(id string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, id)
	})
}

// doRequest drives r.ServeHTTP for the given host/path and returns the recorder.
func doRequest(r *Router, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRouter_ExactHost(t *testing.T) {
	r := NewRouter()
	r.Handle("api.myapp.com", markerHandler("api"))
	if got := doRequest(r, "api.myapp.com", "/anything").Body.String(); got != "api" {
		t.Errorf("body = %q, want api", got)
	}
}

func TestRouter_ExactHostAndPath(t *testing.T) {
	r := NewRouter()
	r.Handle("api.myapp.com/v2/users", markerHandler("users"))
	if got := doRequest(r, "api.myapp.com", "/v2/users").Body.String(); got != "users" {
		t.Errorf("body = %q, want users", got)
	}
	// A different path must not match the exact route → 404.
	if code := doRequest(r, "api.myapp.com", "/v2/other").Code; code != http.StatusNotFound {
		t.Errorf("non-matching path code = %d, want 404", code)
	}
}

func TestRouter_PathPrefix(t *testing.T) {
	r := NewRouter()
	r.Handle("api.myapp.com/v2/*", markerHandler("v2"))
	if got := doRequest(r, "api.myapp.com", "/v2/users/123").Body.String(); got != "v2" {
		t.Errorf("body = %q, want v2", got)
	}
}

func TestRouter_WildcardHostMatches(t *testing.T) {
	r := NewRouter()
	r.Handle("*.myapp.com", markerHandler("wild"))
	if got := doRequest(r, "sub.myapp.com", "/").Body.String(); got != "wild" {
		t.Errorf("body = %q, want wild", got)
	}
}

func TestRouter_WildcardDoesNotMatchApex(t *testing.T) {
	r := NewRouter()
	r.Handle("*.myapp.com", markerHandler("wild"))
	// myapp.com is the apex, not a subdomain → no match → 404.
	if code := doRequest(r, "myapp.com", "/").Code; code != http.StatusNotFound {
		t.Errorf("apex against wildcard code = %d, want 404", code)
	}
}

func TestRouter_ExactBeatsWildcard(t *testing.T) {
	r := NewRouter()
	r.Handle("*.myapp.com", markerHandler("wild"))
	r.Handle("api.myapp.com", markerHandler("exact"))
	if got := doRequest(r, "api.myapp.com", "/").Body.String(); got != "exact" {
		t.Errorf("body = %q, want exact (exact host beats wildcard)", got)
	}
}

func TestRouter_HostPathBeatsHostOnly(t *testing.T) {
	r := NewRouter()
	r.Handle("api.myapp.com", markerHandler("host-only"))
	r.Handle("api.myapp.com/v2/*", markerHandler("host-path"))
	if got := doRequest(r, "api.myapp.com", "/v2/x").Body.String(); got != "host-path" {
		t.Errorf("body = %q, want host-path (host+path beats host-only)", got)
	}
	// A path outside /v2 falls back to the host-only route.
	if got := doRequest(r, "api.myapp.com", "/other").Body.String(); got != "host-only" {
		t.Errorf("body = %q, want host-only", got)
	}
}

func TestRouter_PathOnlyMatchesAnyHost(t *testing.T) {
	r := NewRouter()
	r.Handle("/api/*", markerHandler("path-only"))
	if got := doRequest(r, "whatever.com", "/api/x").Body.String(); got != "path-only" {
		t.Errorf("body = %q, want path-only", got)
	}
	if got := doRequest(r, "other.org", "/api/y").Body.String(); got != "path-only" {
		t.Errorf("body = %q, want path-only", got)
	}
}

func TestRouter_NoMatch404(t *testing.T) {
	r := NewRouter()
	r.Handle("api.myapp.com", markerHandler("api"))
	rec := doRequest(r, "unknown.com", "/x")
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestRouter_404BodyJSON(t *testing.T) {
	r := NewRouter()
	rec := doRequest(r, "nope.com", "/missing")
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 body is not valid JSON: %v\n%s", err, rec.Body.String())
	}
	for _, k := range []string{"error", "host", "path"} {
		if _, ok := body[k]; !ok {
			t.Errorf("404 JSON missing field %q: %v", k, body)
		}
	}
	if body["host"] != "nope.com" || body["path"] != "/missing" {
		t.Errorf("404 JSON host/path = %q/%q, want nope.com//missing", body["host"], body["path"])
	}
}

func TestRouter_RoutesListsAll(t *testing.T) {
	r := NewRouter()
	r.Handle("a.com", markerHandler("a"))
	r.Handle("b.com/x/*", markerHandler("b"))
	r.Handle("/p/*", markerHandler("p"))
	routes := r.Routes()
	if len(routes) != 3 {
		t.Fatalf("Routes() = %d, want 3", len(routes))
	}
	seen := map[string]bool{}
	for _, rt := range routes {
		seen[rt.Pattern] = true
	}
	for _, p := range []string{"a.com", "b.com/x/*", "/p/*"} {
		if !seen[p] {
			t.Errorf("Routes() missing pattern %q", p)
		}
	}
}
