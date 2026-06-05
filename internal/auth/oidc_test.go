package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// mockOIDC is an in-memory OIDC provider for tests: it serves the discovery
// document, a JWKS containing the test RSA public key, and a token endpoint that
// returns a signed ID token for any code except "bad-code".
type mockOIDC struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	subject  string
	email    string
}

func newMockOIDC(t *testing.T) *mockOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockOIDC{key: key, clientID: "vortex-client", subject: "user-123", email: "sso@example.com"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		base := m.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/auth",
			"token_endpoint":                        base + "/token",
			"jwks_uri":                              base + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       m.key.Public(),
			KeyID:     "test-key",
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}}}
		_ = json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("code") == "bad-code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		idToken := m.signIDToken(t, time.Now().Add(time.Hour))
		// oauth2 only parses a JSON token response when the Content-Type says so;
		// otherwise it falls back to form decoding and misses access_token.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-xyz",
			"token_type":   "Bearer",
			"id_token":     idToken,
		})
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

// signIDToken builds a signed RS256 ID token with the standard claims.
func (m *mockOIDC) signIDToken(t *testing.T, exp time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: m.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		t.Fatal(err)
	}
	claimSet := map[string]any{
		"iss":   m.srv.URL,
		"aud":   m.clientID,
		"sub":   m.subject,
		"email": m.email,
		"name":  "SSO User",
		"exp":   exp.Unix(),
		"iat":   time.Now().Unix(),
	}
	signed, err := jwt.Signed(signer).Claims(claimSet).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func newProvider(t *testing.T, m *mockOIDC) *OIDCProvider {
	t.Helper()
	p, err := NewOIDCProvider(context.Background(), OIDCConfig{
		ProviderURL: m.srv.URL,
		ClientID:    m.clientID,
		RedirectURL: "https://vortex.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOIDC_DiscoversEndpoints(t *testing.T) {
	m := newMockOIDC(t)
	if _, err := NewOIDCProvider(context.Background(), OIDCConfig{
		ProviderURL: m.srv.URL, ClientID: m.clientID,
	}); err != nil {
		t.Fatalf("discovery should succeed against mock: %v", err)
	}
}

func TestOIDC_DiscoveryFailsOnBadURL(t *testing.T) {
	if _, err := NewOIDCProvider(context.Background(), OIDCConfig{
		ProviderURL: "http://127.0.0.1:1", ClientID: "x",
	}); err == nil {
		t.Error("expected discovery error for unreachable issuer")
	}
}

func TestOIDC_AuthCodeURL(t *testing.T) {
	m := newMockOIDC(t)
	p := newProvider(t, m)
	u := p.AuthCodeURL("state-abc")
	if !strings.Contains(u, "client_id=vortex-client") {
		t.Errorf("auth URL missing client_id: %s", u)
	}
	if !strings.Contains(u, "state=state-abc") {
		t.Errorf("auth URL missing state: %s", u)
	}
}

func TestOIDC_ExchangeReturnsUser(t *testing.T) {
	m := newMockOIDC(t)
	p := newProvider(t, m)
	user, err := p.Exchange(context.Background(), "good-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if user.Email != "sso@example.com" || user.ID != "user-123" {
		t.Errorf("user = %+v, want sso@example.com / user-123", user)
	}
	if len(user.Roles) != 1 || user.Roles[0] != RoleViewer {
		t.Errorf("SSO user should default to viewer, got %v", user.Roles)
	}
}

func TestOIDC_ExchangeInvalidCode(t *testing.T) {
	m := newMockOIDC(t)
	p := newProvider(t, m)
	if _, err := p.Exchange(context.Background(), "bad-code"); err == nil {
		t.Error("expected error for invalid code")
	}
}

func TestOIDC_MiddlewareNoToken(t *testing.T) {
	m := newMockOIDC(t)
	p := newProvider(t, m)
	h := p.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token status = %d, want 401", rec.Code)
	}
}

func TestOIDC_MiddlewareInvalidToken(t *testing.T) {
	m := newMockOIDC(t)
	p := newProvider(t, m)
	h := p.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid token status = %d, want 401", rec.Code)
	}
}

func TestOIDC_MiddlewareSetsUser(t *testing.T) {
	m := newMockOIDC(t)
	p := newProvider(t, m)

	var gotEmail string
	var present bool
	h := p.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		present = ok
		gotEmail = u.Email
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+m.signIDToken(t, time.Now().Add(time.Hour)))
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("valid token rejected: %d", rec.Code)
	}
	if !present {
		t.Error("middleware did not set user in context")
	}
	if gotEmail != "sso@example.com" {
		t.Errorf("context user email = %q, want sso@example.com", gotEmail)
	}
}

func TestOIDC_UserFromContextEmpty(t *testing.T) {
	if _, ok := UserFromContext(context.Background()); ok {
		t.Error("empty context should not yield a user")
	}
}
