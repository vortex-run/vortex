package policy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// routeNameKey is the context key under which the matched route name is stored
// for the policy middleware to include in its OPA input.
type routeNameKey struct{}

// SetRouteName returns a copy of ctx carrying the matched route name, so the
// policy middleware can expose it to Rego policies as input.route.
func SetRouteName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, routeNameKey{}, name)
}

// RouteNameFromContext returns the route name stored by SetRouteName, or "" if
// none was set.
func RouteNameFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(routeNameKey{}).(string); ok {
		return v
	}
	return ""
}

// NewMiddleware returns an HTTP middleware that evaluates engine's policy on
// every request. On allow it calls the next handler; on deny it returns 403; on
// an evaluation error it returns 500. Both error responses carry a JSON body.
func NewMiddleware(engine *Engine) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			input := buildInput(r)

			allowed, err := engine.Eval(r.Context(), input)
			if err != nil {
				slog.Default().Error("policy evaluation failed",
					"method", r.Method, "path", r.URL.Path, "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error":  "policy evaluation failed",
					"detail": err.Error(),
				})
				return
			}

			if !allowed {
				slog.Default().Warn("request denied by policy",
					"method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "policy denied",
					"path":  r.URL.Path,
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// buildInput constructs the OPA input document from an HTTP request.
func buildInput(r *http.Request) map[string]any {
	headers := make(map[string]string, len(r.Header))
	for name, vals := range r.Header {
		if len(vals) > 0 {
			headers[name] = vals[0]
		}
	}
	return map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"host":    r.Host,
		"headers": headers,
		"remote":  r.RemoteAddr,
		"route":   RouteNameFromContext(r.Context()),
	}
}

// writeJSON writes status and a JSON-encoded body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
