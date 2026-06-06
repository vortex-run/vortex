//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vortex-run/vortex/internal/secrets"
	"github.com/vortex-run/vortex/internal/tenancy"
	"github.com/vortex-run/vortex/internal/testutil"
)

func TestTenancy_NamespaceIsolatesSecrets(t *testing.T) {
	store, err := secrets.NewSecretStore(t.TempDir(), []byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	a := tenancy.NewIsolatedSecretStore(store, "ns-a")
	b := tenancy.NewIsolatedSecretStore(store, "ns-b")

	if err := a.Set("API_KEY", "value-a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("API_KEY", "value-b"); err != nil {
		t.Fatal(err)
	}

	va, _ := a.Get("API_KEY")
	vb, _ := b.Get("API_KEY")
	if va != "value-a" || vb != "value-b" {
		t.Errorf("namespaces not isolated: a=%q b=%q", va, vb)
	}
}

func TestTenancy_QuotaEnforced(t *testing.T) {
	// A namespace limited to 2 connections: holding 2 in flight, a 3rd should be
	// rejected with 429.
	reg := tenancy.NewRegistry()
	if _, err := reg.Create(tenancy.NamespaceConfig{
		ID: "ns-q", OrgID: "org", Quotas: tenancy.QuotaConfig{MaxConnections: 2},
	}); err != nil {
		t.Fatal(err)
	}
	enf := tenancy.NewEnforcer(reg)

	block := make(chan struct{})
	started := make(chan struct{}, 2)
	h := enf.HTTPMiddleware("ns-q")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Hold 2 connections open (fills the quota).
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(srv.URL)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	<-started
	<-started // both in-flight, active=2

	// The 3rd request must be rejected.
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("3rd request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("3rd request status = %d, want 429 (over quota)", resp.StatusCode)
	}
	close(block)
	wg.Wait()
}

func TestTenancy_NoNamespaceRouteUnaffected(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	// A route with no namespace_id has no quota; many requests all succeed.
	for i := 0; i < 50; i++ {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, resp.StatusCode)
		}
	}

	if logs := p.Logs(); !strings.Contains(logs, "tenancy enabled") {
		t.Errorf("startup log should report tenancy enabled:\n%s", logs)
	}
}
