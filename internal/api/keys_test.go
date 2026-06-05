package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

// newAuthedServer builds a Server with auth wired, returning the server and an
// admin API-key secret for authenticating admin requests.
func newAuthedServer(t *testing.T) (*Server, string) {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())

	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, adminSecret, err := keys.Issue("admin", "default", []auth.Role{auth.RoleAdmin}, "test admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	return s, adminSecret
}

// serve dispatches a request through the server's HTTP handler.
func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestAPI_HealthPublicNoAuth(t *testing.T) {
	s, _ := newAuthedServer(t)
	// A non-loopback remote with no credentials must still reach /health.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/health status = %d, want 200 (public)", rec.Code)
	}
}

func TestAPI_KeysRequireAuth(t *testing.T) {
	s, _ := newAuthedServer(t)
	// No credential → 401 on the admin-only key endpoints.
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/keys without auth = %d, want 401", rec.Code)
	}
}

func TestAPI_CreateAndListKey(t *testing.T) {
	s, adminSecret := newAuthedServer(t)

	// Create a new key as admin.
	body, _ := json.Marshal(createKeyRequest{
		UserID: "ci", OrgID: "default", Roles: []auth.Role{auth.RoleOperator}, Description: "ci token",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader(body))
	req.Header.Set("X-API-Key", adminSecret)
	rec := serve(s, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/keys = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created createKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Secret == "" {
		t.Errorf("create response missing id/secret: %+v", created)
	}

	// List keys as admin — the created key (plus the admin key) must appear.
	lreq := httptest.NewRequest(http.MethodGet, "/api/keys?org=default", nil)
	lreq.Header.Set("X-API-Key", adminSecret)
	lrec := serve(s, lreq)
	if lrec.Code != http.StatusOK {
		t.Fatalf("GET /api/keys = %d, want 200", lrec.Code)
	}
	var listResp struct {
		Keys []auth.APIKey `json:"keys"`
	}
	if err := json.Unmarshal(lrec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range listResp.Keys {
		if k.ID == created.ID {
			found = true
		}
		if k.Hash != "" {
			t.Error("listed key must not expose its hash")
		}
	}
	if !found {
		t.Errorf("created key %s not found in list", created.ID)
	}
}

func TestAPI_CreateKeyForbiddenForNonAdmin(t *testing.T) {
	s, _ := newAuthedServer(t)
	// Issue a viewer key directly via the store the server uses. We need a handle
	// to that store, so re-wire with a known store.
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, viewerSecret, _ := keys.Issue("v", "default", []auth.Role{auth.RoleViewer}, "viewer", 0)
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)

	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-API-Key", viewerSecret)
	rec := serve(s, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer POST /api/keys = %d, want 403", rec.Code)
	}
}

func TestAPI_InternalReloadLocalhostNoAuth(t *testing.T) {
	s, _ := newAuthedServer(t)
	s.SetReloadFunc(func() error { return nil })
	// A loopback reload needs no key (control plane); the handler's own
	// localhost check governs access.
	req := httptest.NewRequest(http.MethodPost, "/internal/reload", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Errorf("loopback /internal/reload = %d, want 200", rec.Code)
	}
}
