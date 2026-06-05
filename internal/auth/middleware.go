package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// requiredRoleKey is the context key under which a route's required role is
// stored for the auth middleware to enforce.
type requiredRoleKey struct{}

// SetRequiredRole returns a copy of ctx carrying the role required to access the
// current route. When unset, any authenticated user is allowed.
func SetRequiredRole(ctx context.Context, role Role) context.Context {
	return context.WithValue(ctx, requiredRoleKey{}, role)
}

// RequiredRoleFromContext returns the required role set by SetRequiredRole.
func RequiredRoleFromContext(ctx context.Context) (Role, bool) {
	role, ok := ctx.Value(requiredRoleKey{}).(Role)
	return role, ok
}

// NewAuthMiddleware builds the management-API authentication+authorization
// middleware. Authentication is attempted in this order:
//
//  1. Authorization: Bearer <token> — verified as an OIDC token (if oidc is
//     configured), otherwise as an API-key secret.
//  2. X-API-Key: <secret> — verified as an API-key secret.
//
// A request with no usable credential gets 401. Once authenticated, if the
// route declares a required role (SetRequiredRole) the user must hold it via
// rbac.Can(..manage..); otherwise any authenticated user passes. An authz
// failure gets 403.
func NewAuthMiddleware(keys *APIKeyStore, oidc *OIDCProvider, rbac *RBAC) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := authenticate(r, keys, oidc)
			if !ok {
				writeAuthError(w, http.StatusUnauthorized, "authentication required")
				return
			}

			// Authorization: enforce the route's required role if one is set.
			if required, has := RequiredRoleFromContext(r.Context()); has {
				if !userHasRole(user, required) && !rbac.Can(user, ActionManage, ResourceConfig) {
					slog.Default().Warn("request denied: insufficient role",
						"user", user.ID, "required", required, "path", r.URL.Path)
					writeAuthError(w, http.StatusForbidden, "insufficient role")
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
		})
	}
}

// authenticate resolves the requesting User from the request's credentials,
// trying OIDC bearer tokens first (when configured) and then API keys (Bearer or
// X-API-Key).
func authenticate(r *http.Request, keys *APIKeyStore, oidc *OIDCProvider) (User, bool) {
	if raw, ok := bearerToken(r); ok {
		// Prefer OIDC verification when an OIDC provider is configured.
		if oidc != nil {
			if user, err := oidc.userFromRawIDToken(r.Context(), raw); err == nil {
				return user, true
			}
		}
		if user, ok := userFromAPIKey(keys, raw); ok {
			return user, true
		}
	}

	if secret := r.Header.Get("X-API-Key"); secret != "" {
		if user, ok := userFromAPIKey(keys, secret); ok {
			return user, true
		}
	}

	return User{}, false
}

// userFromAPIKey verifies an API-key secret and maps it to a User.
func userFromAPIKey(keys *APIKeyStore, secret string) (User, bool) {
	if keys == nil {
		return User{}, false
	}
	key, err := keys.Verify(secret)
	if err != nil {
		// Any verification failure (not found, expired, malformed) is simply
		// "not authenticated" — we do not distinguish to the caller.
		return User{}, false
	}
	return User{
		ID:    key.UserID,
		OrgID: key.OrgID,
		Roles: key.Roles,
	}, true
}

// userHasRole reports whether the user directly holds the named role.
func userHasRole(user User, role Role) bool {
	for _, r := range user.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// writeAuthError writes a JSON error body with the given status.
func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
