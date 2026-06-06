package tenancy

import (
	"errors"
	"path/filepath"
	"testing"
)

func cfg(id, org string) NamespaceConfig {
	return NamespaceConfig{ID: id, Name: id, OrgID: org, Quotas: QuotaConfig{MaxRoutes: 10}}
}

func TestRegistry_CreateStores(t *testing.T) {
	r := NewRegistry()
	ns, err := r.Create(cfg("ns-1", "org-a"))
	if err != nil {
		t.Fatal(err)
	}
	if ns.ID() != "ns-1" {
		t.Errorf("created ID = %q", ns.ID())
	}
	if len(r.List("")) != 1 {
		t.Errorf("registry should hold 1 namespace")
	}
}

func TestRegistry_CreateDuplicateErrors(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Create(cfg("ns-1", "org-a"))
	if _, err := r.Create(cfg("ns-1", "org-a")); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate create = %v, want ErrAlreadyExists", err)
	}
}

func TestRegistry_GetRetrieves(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Create(cfg("ns-1", "org-a"))
	ns, err := r.Get("ns-1")
	if err != nil || ns.OrgID() != "org-a" {
		t.Errorf("Get = %v, %v", ns, err)
	}
}

func TestRegistry_GetMissingNotFound(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}
}

func TestRegistry_ListByOrg(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Create(cfg("a1", "org-a"))
	_, _ = r.Create(cfg("a2", "org-a"))
	_, _ = r.Create(cfg("b1", "org-b"))
	if got := len(r.List("org-a")); got != 2 {
		t.Errorf("org-a list = %d, want 2", got)
	}
	if got := len(r.List("org-b")); got != 1 {
		t.Errorf("org-b list = %d, want 1", got)
	}
}

func TestRegistry_ListAllEmptyOrg(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Create(cfg("a1", "org-a"))
	_, _ = r.Create(cfg("b1", "org-b"))
	if got := len(r.List("")); got != 3-1 {
		t.Errorf("all list = %d, want 2", got)
	}
}

func TestRegistry_Delete(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Create(cfg("ns-1", "org-a"))
	if err := r.Delete("ns-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get("ns-1"); !errors.Is(err, ErrNotFound) {
		t.Error("namespace should be gone after Delete")
	}
	if err := r.Delete("ns-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing = %v, want ErrNotFound", err)
	}
}

func TestRegistry_UpdateQuotas(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Create(cfg("ns-1", "org-a"))
	err := r.Update("ns-1", NamespaceConfig{Name: "Renamed", Quotas: QuotaConfig{MaxRoutes: 99}})
	if err != nil {
		t.Fatal(err)
	}
	ns, _ := r.Get("ns-1")
	if ns.Quotas().MaxRoutes != 99 || ns.Name() != "Renamed" {
		t.Errorf("update not applied: %+v", ns.Config())
	}
	if ns.OrgID() != "org-a" {
		t.Errorf("OrgID should be preserved, got %q", ns.OrgID())
	}
}

func TestRegistry_UpdateCannotChangeOrg(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Create(cfg("ns-1", "org-a"))
	err := r.Update("ns-1", NamespaceConfig{OrgID: "org-b", Quotas: QuotaConfig{}})
	if err == nil {
		t.Error("changing OrgID should error")
	}
}

func TestRegistry_SaveLoadRoundTrip(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Create(NamespaceConfig{ID: "ns-1", Name: "One", OrgID: "org-a", Quotas: QuotaConfig{MaxRoutes: 7, MaxConnections: 50}})
	_, _ = r.Create(cfg("ns-2", "org-b"))
	path := filepath.Join(t.TempDir(), "ns.json")
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}

	r2 := NewRegistry()
	if err := r2.Load(path); err != nil {
		t.Fatal(err)
	}
	if len(r2.List("")) != 2 {
		t.Fatalf("reloaded count = %d, want 2", len(r2.List("")))
	}
	ns, _ := r2.Get("ns-1")
	if ns.Quotas().MaxRoutes != 7 || ns.Quotas().MaxConnections != 50 || ns.Name() != "One" {
		t.Errorf("reloaded ns-1 = %+v", ns.Config())
	}
}

func TestRegistry_LoadMissingFileEmpty(t *testing.T) {
	r := NewRegistry()
	if err := r.Load(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Errorf("Load missing file = %v, want nil", err)
	}
}
