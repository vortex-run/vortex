package tenancy

import (
	"context"
	"strings"
	"time"

	"github.com/vortex-run/vortex/internal/observability"
	"github.com/vortex-run/vortex/internal/secrets"
)

// nsContextKey is the context key under which the active namespace is stored.
type nsContextKey struct{}

// WithNamespace returns a copy of ctx carrying ns.
func WithNamespace(ctx context.Context, ns *Namespace) context.Context {
	return context.WithValue(ctx, nsContextKey{}, ns)
}

// NamespaceFromContext returns the namespace stored in ctx, if any.
func NamespaceFromContext(ctx context.Context) (*Namespace, bool) {
	ns, ok := ctx.Value(nsContextKey{}).(*Namespace)
	return ns, ok
}

// NamespaceIDFromContext returns the namespace ID in ctx, or "".
func NamespaceIDFromContext(ctx context.Context) string {
	if ns, ok := NamespaceFromContext(ctx); ok {
		return ns.ID()
	}
	return ""
}

// secretKeySeparator separates a namespace prefix from a secret name. Secret
// names allow only [A-Za-z0-9_], so the namespace ID is sanitized (hyphens to
// underscores) and a double underscore marks the boundary.
const secretKeySeparator = "__"

// IsolatedSecretStore wraps a SecretStore, prefixing every key with a sanitized
// namespace ID so secrets in different namespaces never collide and one
// namespace cannot read another's secrets.
type IsolatedSecretStore struct {
	store  *secrets.SecretStore
	prefix string
}

// NewIsolatedSecretStore wraps store for namespace nsID.
func NewIsolatedSecretStore(store *secrets.SecretStore, nsID string) *IsolatedSecretStore {
	return &IsolatedSecretStore{
		store:  store,
		prefix: sanitizeNS(nsID) + secretKeySeparator,
	}
}

// Set stores value under the namespace-prefixed key.
func (s *IsolatedSecretStore) Set(name, value string) error {
	return s.store.Set(s.prefix+name, value)
}

// Get retrieves the namespace-prefixed key.
func (s *IsolatedSecretStore) Get(name string) (string, error) {
	return s.store.Get(s.prefix + name)
}

// Delete removes the namespace-prefixed key.
func (s *IsolatedSecretStore) Delete(name string) error {
	return s.store.Delete(s.prefix + name)
}

// Exists reports whether the namespace-prefixed key is set.
func (s *IsolatedSecretStore) Exists(name string) (bool, error) {
	return s.store.Exists(s.prefix + name)
}

// List returns this namespace's secret names with the prefix stripped, so a
// namespace sees only its own keys.
func (s *IsolatedSecretStore) List() ([]string, error) {
	all, err := s.store.List()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range all {
		if strings.HasPrefix(k, s.prefix) {
			out = append(out, strings.TrimPrefix(k, s.prefix))
		}
	}
	return out, nil
}

// IsolatedMetrics wraps observability.Metrics, prefixing the route label with
// the namespace ID so each namespace's metrics are attributable to it.
type IsolatedMetrics struct {
	metrics *observability.Metrics
	nsID    string
}

// NewIsolatedMetrics wraps metrics for namespace nsID.
func NewIsolatedMetrics(metrics *observability.Metrics, nsID string) *IsolatedMetrics {
	return &IsolatedMetrics{metrics: metrics, nsID: nsID}
}

// label namespaces a route name as "<nsID>/<route>".
func (m *IsolatedMetrics) label(route string) string {
	return m.nsID + "/" + route
}

// RecordRequest records a request under the namespaced route label.
func (m *IsolatedMetrics) RecordRequest(route, method string, status int, dur time.Duration) {
	m.metrics.RecordRequest(m.label(route), method, status, dur)
}

// SetActiveConns sets the active-connection gauge under the namespaced label.
func (m *IsolatedMetrics) SetActiveConns(route, protocol string, n int64) {
	m.metrics.SetActiveConns(m.label(route), protocol, n)
}

// RecordBytes records bytes under the namespaced label.
func (m *IsolatedMetrics) RecordBytes(route string, in, out int64) {
	m.metrics.RecordBytes(m.label(route), in, out)
}

// sanitizeNS replaces hyphens with underscores so a namespace ID is a valid
// secret-name component.
func sanitizeNS(nsID string) string {
	return strings.ReplaceAll(nsID, "-", "_")
}
