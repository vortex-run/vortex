package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// issueKey issues an API key with the given roles and returns its secret.
func issueKey(t *testing.T, keys *APIKeyStore, roles ...Role) string {
	t.Helper()
	_, secret, err := keys.Issue("u1", "o1", roles, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// reachHandler is a handler that records that it was reached and returns 200.
func reachHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthMW_NoAuthReturns401(t *testing.T) {
	keys := NewAPIKeyStore()
	mw := NewAuthMiddleware(keys, nil, NewRBAC())
	var reached bool
	h := mw(reachHandler(&reached))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if reached {
		t.Error("handler should not be reached without auth")
	}
}

func TestAuthMW_ValidAPIKeyPasses(t *testing.T) {
	keys := NewAPIKeyStore()
	secret := issueKey(t, keys, RoleViewer)
	mw := NewAuthMiddleware(keys, nil, NewRBAC())
	var reached bool
	h := mw(reachHandler(&reached))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !reached {
		t.Errorf("valid api key: status=%d reached=%v, want 200 true", rec.Code, reached)
	}
}

func TestAuthMW_InvalidAPIKeyReturns401(t *testing.T) {
	keys := NewAPIKeyStore()
	_ = issueKey(t, keys, RoleViewer)
	mw := NewAuthMiddleware(keys, nil, NewRBAC())
	h := mw(reachHandler(new(bool)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer deadbeef.bogus")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMW_ValidOIDCTokenPasses(t *testing.T) {
	m := newMockOIDC(t)
	oidc := newProvider(t, m)
	mw := NewAuthMiddleware(NewAPIKeyStore(), oidc, NewRBAC())
	var reached bool
	h := mw(reachHandler(&reached))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+m.signIDToken(t, time.Now().Add(time.Hour)))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !reached {
		t.Errorf("valid OIDC token: status=%d reached=%v, want 200 true", rec.Code, reached)
	}
}

func TestAuthMW_InsufficientRoleReturns403(t *testing.T) {
	keys := NewAPIKeyStore()
	secret := issueKey(t, keys, RoleViewer) // viewer, but route needs admin
	mw := NewAuthMiddleware(keys, nil, NewRBAC())
	var reached bool

	// Wrap so the route declares an admin requirement in context.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(SetRequiredRole(r.Context(), RoleAdmin))
		mw(reachHandler(&reached)).ServeHTTP(w, r)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (viewer lacks admin)", rec.Code)
	}
	if reached {
		t.Error("handler should not be reached on authz failure")
	}
}

func TestAuthMW_SufficientRolePasses(t *testing.T) {
	keys := NewAPIKeyStore()
	secret := issueKey(t, keys, RoleAdmin)
	mw := NewAuthMiddleware(keys, nil, NewRBAC())
	var reached bool

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(SetRequiredRole(r.Context(), RoleAdmin))
		mw(reachHandler(&reached)).ServeHTTP(w, r)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !reached {
		t.Errorf("admin with admin requirement: status=%d reached=%v, want 200 true", rec.Code, reached)
	}
}

func TestAuthMW_XAPIKeyHeaderWorks(t *testing.T) {
	keys := NewAPIKeyStore()
	secret := issueKey(t, keys, RoleOperator)
	mw := NewAuthMiddleware(keys, nil, NewRBAC())
	var reached bool
	h := mw(reachHandler(&reached))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", secret)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !reached {
		t.Errorf("X-API-Key: status=%d reached=%v, want 200 true", rec.Code, reached)
	}
}
