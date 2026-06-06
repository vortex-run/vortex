package tenancy

import (
	"errors"
	"testing"
)

func TestNamespace_NewCreates(t *testing.T) {
	ns, err := NewNamespace(NamespaceConfig{
		ID: "ns-1", Name: "Team One", OrgID: "org-a",
		Quotas: QuotaConfig{MaxRoutes: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ns.ID() != "ns-1" || ns.Name() != "Team One" || ns.OrgID() != "org-a" {
		t.Errorf("fields wrong: id=%q name=%q org=%q", ns.ID(), ns.Name(), ns.OrgID())
	}
	if ns.Quotas().MaxRoutes != 10 {
		t.Errorf("MaxRoutes = %d, want 10", ns.Quotas().MaxRoutes)
	}
}

func TestNamespace_EmptyIDErrors(t *testing.T) {
	if _, err := NewNamespace(NamespaceConfig{OrgID: "org-a"}); err == nil {
		t.Error("empty ID should error")
	}
}

func TestNamespace_EmptyOrgErrors(t *testing.T) {
	if _, err := NewNamespace(NamespaceConfig{ID: "ns-1"}); err == nil {
		t.Error("empty OrgID should error")
	}
}

func TestNamespace_InvalidIDChars(t *testing.T) {
	for _, bad := range []string{"ns 1", "ns/1", "ns_1", "ns@1", "ns.1"} {
		if _, err := NewNamespace(NamespaceConfig{ID: bad, OrgID: "o"}); err == nil {
			t.Errorf("ID %q should be rejected", bad)
		}
	}
	// Valid IDs are accepted.
	for _, ok := range []string{"ns-1", "ns1", "NS-1-a"} {
		if _, err := NewNamespace(NamespaceConfig{ID: ok, OrgID: "o"}); err != nil {
			t.Errorf("ID %q should be accepted: %v", ok, err)
		}
	}
}

func TestNamespace_CheckQuotaUnderLimit(t *testing.T) {
	ns, _ := NewNamespace(NamespaceConfig{
		ID: "ns", OrgID: "o", Quotas: QuotaConfig{MaxRoutes: 5, MaxConnections: 100},
	})
	if err := ns.CheckQuota("routes", 3); err != nil {
		t.Errorf("3 < 5 routes should be allowed: %v", err)
	}
	if err := ns.CheckQuota("connections", 99); err != nil {
		t.Errorf("99 < 100 connections should be allowed: %v", err)
	}
}

func TestNamespace_CheckQuotaOverLimit(t *testing.T) {
	ns, _ := NewNamespace(NamespaceConfig{
		ID: "ns", OrgID: "o", Quotas: QuotaConfig{MaxRoutes: 5},
	})
	err := ns.CheckQuota("routes", 5)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("at-limit (5/5) should exceed: %v", err)
	}
	if err := ns.CheckQuota("routes", 10); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("over-limit should exceed: %v", err)
	}
}

func TestNamespace_CheckQuotaZeroUnlimited(t *testing.T) {
	ns, _ := NewNamespace(NamespaceConfig{
		ID: "ns", OrgID: "o", Quotas: QuotaConfig{BandwidthMbps: 0},
	})
	// A 0 limit means unlimited, so any usage is allowed.
	if err := ns.CheckQuota("bandwidth", 1_000_000); err != nil {
		t.Errorf("0 limit should be unlimited: %v", err)
	}
}

func TestNamespace_QuotasAccessible(t *testing.T) {
	q := QuotaConfig{MaxRoutes: 1, MaxSecrets: 2, MaxConnections: 3, BandwidthMbps: 4, MaxAgents: 5}
	ns, _ := NewNamespace(NamespaceConfig{ID: "ns", OrgID: "o", Quotas: q})
	if ns.Quotas() != q {
		t.Errorf("Quotas = %+v, want %+v", ns.Quotas(), q)
	}
}

func TestNamespace_CheckQuotaUnknownResource(t *testing.T) {
	ns, _ := NewNamespace(NamespaceConfig{ID: "ns", OrgID: "o"})
	if err := ns.CheckQuota("bogus", 1); err == nil {
		t.Error("unknown resource should error")
	}
}
