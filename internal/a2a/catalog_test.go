package a2a

import (
	"strings"
	"testing"
)

func TestNewCatalog_RejectsDuplicateAndInvalid(t *testing.T) {
	_, err := NewCatalog([]AgentManifest{
		{ID: "a", Name: "A", Version: "1", Capabilities: []string{"x"}},
		{ID: "a", Name: "B", Version: "1", Capabilities: []string{"y"}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate id: %v", err)
	}
	_, err = NewCatalog([]AgentManifest{{ID: "", Name: "A", Version: "1", Capabilities: []string{"x"}}})
	if err == nil {
		t.Fatal("empty id should fail")
	}
	_, err = NewCatalog([]AgentManifest{{ID: "a", Name: "A", Version: "1"}})
	if err == nil {
		t.Fatal("missing capabilities should fail")
	}
}

func TestCatalog_SelectByCapabilityAndProfile(t *testing.T) {
	c := DefaultCatalog()
	got := c.Select(SelectQuery{RequiredCapabilities: []string{"code.edit"}})
	if len(got) != 1 || got[0].ID != "code-agent" {
		t.Fatalf("select code.edit = %+v", got)
	}
	got = c.Select(SelectQuery{
		RequiredCapabilities: []string{"code.read"},
		PermissionProfile:    "read-mostly",
	})
	if len(got) != 1 || got[0].ID != "review-agent" {
		t.Fatalf("select code.read+read-mostly = %+v", got)
	}
	got = c.Select(SelectQuery{RequiredCapabilities: []string{"no.such.cap"}})
	if len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}

func TestValidateDelegation_Bounds(t *testing.T) {
	base := DelegationRequest{
		ChildObjective:      "fix bug",
		AllowedCapabilities: []string{"code.edit"},
		Depth:               0,
		SiblingCount:        0,
	}
	if err := ValidateDelegation(base); err != nil {
		t.Fatal(err)
	}
	deep := base
	deep.Depth = DefaultMaxDelegationDepth
	if err := ValidateDelegation(deep); err == nil {
		t.Fatal("depth at max should fail")
	}
	wide := base
	wide.SiblingCount = DefaultMaxChildFanOut
	if err := ValidateDelegation(wide); err == nil {
		t.Fatal("fan-out at max should fail")
	}
}

func TestChildCompatible_GrantMustBeDeclared(t *testing.T) {
	c := DefaultCatalog()
	code, _ := c.Get("code-agent")
	req := DelegationRequest{
		ChildObjective:      "edit",
		AllowedCapabilities: []string{"code.edit"},
		Depth:               0,
	}
	if err := ChildCompatible(code, req); err != nil {
		t.Fatal(err)
	}
	req.AllowedCapabilities = []string{"devops.ssh"}
	if err := ChildCompatible(code, req); err == nil {
		t.Fatal("undeclared grant should fail")
	}
}

func TestResolveOrchestration_MatchAndTeamOnly(t *testing.T) {
	c := DefaultCatalog()
	_, route, err := c.ResolveOrchestration(SelectQuery{RequiredCapabilities: []string{"research.web"}})
	if err != nil || route != "research" {
		t.Fatalf("research: route=%q err=%v", route, err)
	}
	_, _, err = c.ResolveOrchestration(SelectQuery{RequiredCapabilities: []string{"code.edit"}})
	if err == nil || !strings.Contains(err.Error(), "team-only") {
		t.Fatalf("code agent should be team-only: %v", err)
	}
	_, _, err = c.ResolveOrchestration(SelectQuery{RequiredCapabilities: []string{"nope.cap"}})
	if err == nil || !strings.Contains(err.Error(), "no agent matches") {
		t.Fatalf("missing cap: %v", err)
	}
}

func TestResolveOrchestration_PermissionProfile(t *testing.T) {
	c := DefaultCatalog()
	// read-mostly + code.read → review-agent only (code-agent uses local-workspace).
	_, _, err := c.ResolveOrchestration(SelectQuery{
		RequiredCapabilities: []string{"code.read"},
		PermissionProfile:    "read-mostly",
	})
	if err == nil || !strings.Contains(err.Error(), "team-only") {
		t.Fatalf("review-agent is team-only: %v", err)
	}
	_, _, err = c.ResolveOrchestration(SelectQuery{
		RequiredCapabilities: []string{"code.read"},
		PermissionProfile:    "sandbox",
	})
	if err == nil || !strings.Contains(err.Error(), "no agent matches") {
		t.Fatalf("permission mismatch should not match: %v", err)
	}
}

func TestResolveTeam_Specialists(t *testing.T) {
	c := DefaultCatalog()
	_, role, err := c.ResolveTeam(SelectQuery{RequiredCapabilities: []string{"code.edit"}})
	if err != nil || role != "coder" {
		t.Fatalf("coder: role=%q err=%v", role, err)
	}
	_, _, err = c.ResolveTeam(SelectQuery{RequiredCapabilities: []string{"research.web"}})
	if err == nil || !strings.Contains(err.Error(), "not registered on the specialist team") {
		t.Fatalf("research not on team: %v", err)
	}
}

func TestDefaultCatalog_ListDeterministic(t *testing.T) {
	c := DefaultCatalog()
	list := c.List()
	if len(list) < 8 {
		t.Fatalf("want at least 8 manifests, got %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].ID >= list[i].ID {
			t.Fatalf("list not sorted: %q then %q", list[i-1].ID, list[i].ID)
		}
	}
}
