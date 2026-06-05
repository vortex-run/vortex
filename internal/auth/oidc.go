package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// userContextKey is the context key under which an authenticated User is stored.
type userContextKey struct{}

// OIDCConfig configures the OIDC/SSO login flow.
type OIDCConfig struct {
	ProviderURL  string   // issuer URL, e.g. https://accounts.google.com
	ClientID     string   // OAuth2 client ID
	ClientSecret string   // OAuth2 client secret
	RedirectURL  string   // callback URL registered with the provider
	Scopes       []string // requested scopes; defaults to openid, email, profile
}

// OIDCProvider wraps an OIDC issuer: the discovered provider, an ID-token
// verifier, and the OAuth2 config used to build auth URLs and exchange codes.
type OIDCProvider struct {
	cfg      OIDCConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth2   oauth2.Config
}

// NewOIDCProvider discovers the issuer's endpoints from cfg.ProviderURL and
// builds a provider ready to issue auth URLs, exchange codes, and verify tokens.
// It returns an error if discovery fails.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	if cfg.ProviderURL == "" {
		return nil, errors.New("auth: OIDC ProviderURL is required")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("auth: OIDC ClientID is required")
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}

	provider, err := oidc.NewProvider(ctx, cfg.ProviderURL)
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery failed: %w", err)
	}

	return &OIDCProvider{
		cfg:      cfg,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth2: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       cfg.Scopes,
		},
	}, nil
}

// AuthCodeURL returns the provider's authorization URL for the given state,
// which the client redirects the user to in order to log in.
func (p *OIDCProvider) AuthCodeURL(state string) string {
	return p.oauth2.AuthCodeURL(state)
}

// Exchange exchanges an authorization code for tokens, verifies the ID token,
// and maps its claims onto a User. New SSO users get the viewer role by default.
func (p *OIDCProvider) Exchange(ctx context.Context, code string) (User, error) {
	token, err := p.oauth2.Exchange(ctx, code)
	if err != nil {
		return User{}, fmt.Errorf("auth: code exchange failed: %w", err)
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok || rawID == "" {
		return User{}, errors.New("auth: no id_token in token response")
	}
	return p.userFromRawIDToken(ctx, rawID)
}

// claims is the subset of ID-token claims VORTEX consumes.
type claims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}

// userFromRawIDToken verifies a raw ID token and maps its claims to a User.
func (p *OIDCProvider) userFromRawIDToken(ctx context.Context, rawID string) (User, error) {
	idToken, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return User{}, fmt.Errorf("auth: id token verification failed: %w", err)
	}
	var c claims
	if err := idToken.Claims(&c); err != nil {
		return User{}, fmt.Errorf("auth: decoding id token claims: %w", err)
	}
	return User{
		ID:    c.Subject,
		Email: c.Email,
		Roles: []Role{RoleViewer}, // SSO users default to viewer
	}, nil
}

// Middleware authenticates requests by verifying a bearer ID token against the
// OIDC provider and storing the resulting User in the request context. Requests
// without a valid token receive 401.
func (p *OIDCProvider) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
				return
			}
			user, err := p.userFromRawIDToken(r.Context(), raw)
			if err != nil {
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
		})
	}
}

// bearerToken extracts a bearer token from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return h[len(prefix):], true
	}
	return "", false
}

// WithUser returns a copy of ctx carrying the authenticated user.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userContextKey{}, u)
}

// UserFromContext returns the authenticated user stored in ctx, if any.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userContextKey{}).(User)
	return u, ok
}
