package tenancy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/observability"
	"github.com/vortex-run/vortex/internal/secrets"
)

// scrapeMetrics returns the Prometheus exposition text from a Metrics handler.
func scrapeMetrics(t *testing.T, m *observability.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func newSecretStore(t *testing.T) *secrets.SecretStore {
	t.Helper()
	s, err := secrets.NewSecretStore(filepath.Join(t.TempDir(), "secrets"), []byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mkNS(t *testing.T, id string) *Namespace {
	t.Helper()
	ns, err := NewNamespace(NamespaceConfig{ID: id, OrgID: "org-a"})
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

func TestIsolation_ContextRoundTrip(t *testing.T) {
	ns := mkNS(t, "ns-1")
	ctx := WithNamespace(context.Background(), ns)
	got, ok := NamespaceFromContext(ctx)
	if !ok || got.ID() != "ns-1" {
		t.Errorf("context round-trip failed: %v, %v", got, ok)
	}
	if NamespaceIDFromContext(ctx) != "ns-1" {
		t.Errorf("NamespaceIDFromContext = %q", NamespaceIDFromContext(ctx))
	}
}

func TestIsolation_IDFromEmptyContext(t *testing.T) {
	if id := NamespaceIDFromContext(context.Background()); id != "" {
		t.Errorf("empty context ID = %q, want ''", id)
	}
	if _, ok := NamespaceFromContext(context.Background()); ok {
		t.Error("empty context should not yield a namespace")
	}
}

func TestIsolation_SecretStorePrefixes(t *testing.T) {
	store := newSecretStore(t)
	iso := NewIsolatedSecretStore(store, "ns-1")
	if err := iso.Set("DB_PASSWORD", "secret"); err != nil {
		t.Fatal(err)
	}
	// The underlying store holds a prefixed key, not the bare name.
	if ok, _ := store.Exists("DB_PASSWORD"); ok {
		t.Error("underlying store should not have the unprefixed key")
	}
	if ok, _ := store.Exists("ns_1__DB_PASSWORD"); !ok {
		t.Error("underlying store should hold the prefixed key")
	}
	got, err := iso.Get("DB_PASSWORD")
	if err != nil || got != "secret" {
		t.Errorf("Get = %q, %v", got, err)
	}
}

func TestIsolation_SecretsCannotCrossNamespaces(t *testing.T) {
	store := newSecretStore(t)
	a := NewIsolatedSecretStore(store, "ns-a")
	b := NewIsolatedSecretStore(store, "ns-b")

	_ = a.Set("KEY", "value-a")
	// b must not see a's secret.
	if _, err := b.Get("KEY"); err == nil {
		t.Error("namespace b should not read namespace a's secret")
	}
	if ok, _ := b.Exists("KEY"); ok {
		t.Error("namespace b Exists should be false for a's key")
	}
}

func TestIsolation_SameNameDifferentValues(t *testing.T) {
	store := newSecretStore(t)
	a := NewIsolatedSecretStore(store, "ns-a")
	b := NewIsolatedSecretStore(store, "ns-b")

	_ = a.Set("TOKEN", "aaa")
	_ = b.Set("TOKEN", "bbb")

	va, _ := a.Get("TOKEN")
	vb, _ := b.Get("TOKEN")
	if va != "aaa" || vb != "bbb" {
		t.Errorf("isolated values wrong: a=%q b=%q", va, vb)
	}

	// List returns only the namespace's own keys, prefix stripped.
	la, _ := a.List()
	if len(la) != 1 || la[0] != "TOKEN" {
		t.Errorf("namespace a List = %v, want [TOKEN]", la)
	}
}

func TestIsolation_MetricsNamespaceLabel(t *testing.T) {
	m := observability.NewMetrics("vortex")
	iso := NewIsolatedMetrics(m, "ns-1")
	iso.RecordRequest("web", "GET", 200, 0)

	rec := scrapeMetrics(t, m)
	if !strings.Contains(rec, `route="ns-1/web"`) {
		t.Errorf("metric should carry namespaced route label:\n%s", rec)
	}
}
