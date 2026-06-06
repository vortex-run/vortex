package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/vortex-run/vortex/internal/config"
)

// nsServer builds a server with in-memory namespace hooks reachable from
// loopback without a key.
func nsServer(t *testing.T) *Server {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())

	var mu sync.Mutex
	store := map[string]NamespaceInfo{}
	s.SetNamespaceHooks(
		func() []NamespaceInfo {
			mu.Lock()
			defer mu.Unlock()
			out := []NamespaceInfo{}
			for _, n := range store {
				out = append(out, n)
			}
			return out
		},
		func(ni NamespaceInfo) error {
			mu.Lock()
			defer mu.Unlock()
			store[ni.ID] = ni
			return nil
		},
		func(id string) error {
			mu.Lock()
			defer mu.Unlock()
			if _, ok := store[id]; !ok {
				return errNotFound
			}
			delete(store, id)
			return nil
		},
		func(id string) (NamespaceStats, bool) {
			mu.Lock()
			defer mu.Unlock()
			if _, ok := store[id]; !ok {
				return NamespaceStats{}, false
			}
			return NamespaceStats{ActiveConns: 0, RouteCount: 0}, true
		},
	)
	return s
}

// errNotFound is a sentinel for the test deleter.
var errNotFound = &nsErr{"not found"}

type nsErr struct{ s string }

func (e *nsErr) Error() string { return e.s }

func TestAPI_NamespacesEmptyList(t *testing.T) {
	s := nsServer(t)
	rec := serve(s, loopbackReq(http.MethodGet, "/api/namespaces"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Namespaces []NamespaceInfo `json:"namespaces"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Namespaces) != 0 {
		t.Errorf("namespaces = %v, want empty", body.Namespaces)
	}
}

func TestAPI_NamespaceCreate(t *testing.T) {
	s := nsServer(t)
	payload, _ := json.Marshal(map[string]any{"id": "ns-1", "name": "One", "org_id": "org-a"})
	req := loopbackReq(http.MethodPost, "/api/namespaces")
	req.Body = httpBody(payload)
	rec := serve(s, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	// It now appears in the list.
	lrec := serve(s, loopbackReq(http.MethodGet, "/api/namespaces"))
	if !bytes.Contains(lrec.Body.Bytes(), []byte(`"id":"ns-1"`)) {
		t.Errorf("created namespace not listed:\n%s", lrec.Body.String())
	}
}

func TestAPI_NamespaceDelete(t *testing.T) {
	s := nsServer(t)
	payload, _ := json.Marshal(map[string]any{"id": "ns-1", "org_id": "org-a"})
	creq := loopbackReq(http.MethodPost, "/api/namespaces")
	creq.Body = httpBody(payload)
	_ = serve(s, creq)

	drec := serve(s, loopbackReq(http.MethodDelete, "/api/namespaces/ns-1"))
	if drec.Code != http.StatusOK {
		t.Errorf("delete status = %d, want 200", drec.Code)
	}
	// Deleting again is a 404.
	drec2 := serve(s, loopbackReq(http.MethodDelete, "/api/namespaces/ns-1"))
	if drec2.Code != http.StatusNotFound {
		t.Errorf("delete missing status = %d, want 404", drec2.Code)
	}
}

func TestAPI_NamespaceStats(t *testing.T) {
	s := nsServer(t)
	payload, _ := json.Marshal(map[string]any{"id": "ns-1", "org_id": "org-a"})
	creq := loopbackReq(http.MethodPost, "/api/namespaces")
	creq.Body = httpBody(payload)
	_ = serve(s, creq)

	rec := serve(s, loopbackReq(http.MethodGet, "/api/namespaces/ns-1/stats"))
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status = %d, want 200", rec.Code)
	}
	var stats NamespaceStats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	// Stats for a missing namespace is 404.
	miss := serve(s, loopbackReq(http.MethodGet, "/api/namespaces/nope/stats"))
	if miss.Code != http.StatusNotFound {
		t.Errorf("missing stats status = %d, want 404", miss.Code)
	}
}

// httpBody wraps bytes in a ReadCloser for a request body.
func httpBody(b []byte) readCloser { return readCloser{bytes.NewReader(b)} }

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }
