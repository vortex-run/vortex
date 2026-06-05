package auth

import (
	"sync"
	"testing"
)

func userWith(roles ...Role) User {
	return User{ID: "u1", Email: "u@x.com", OrgID: "o1", Roles: roles}
}

func TestRBAC_AdminCanDoEverything(t *testing.T) {
	r := NewRBAC()
	admin := userWith(RoleAdmin)
	for _, res := range allResources {
		for _, act := range allActions {
			if !r.Can(admin, act, res) {
				t.Errorf("admin should be able to %s %s", act, res)
			}
		}
	}
}

func TestRBAC_ViewerCannotWrite(t *testing.T) {
	r := NewRBAC()
	viewer := userWith(RoleViewer)
	for _, res := range allResources {
		if r.Can(viewer, ActionWrite, res) {
			t.Errorf("viewer should NOT be able to write %s", res)
		}
	}
}

func TestRBAC_ReadonlyCannotAccessSecrets(t *testing.T) {
	r := NewRBAC()
	ro := userWith(RoleReadonly)
	if r.Can(ro, ActionRead, ResourceSecrets) {
		t.Error("readonly should NOT have read access to secrets")
	}
	// But it can read routes/config.
	if !r.Can(ro, ActionRead, ResourceRoutes) {
		t.Error("readonly should read routes")
	}
}

func TestRBAC_OperatorCanDeployRoutes(t *testing.T) {
	r := NewRBAC()
	op := userWith(RoleOperator)
	if !r.Can(op, ActionDeploy, ResourceRoutes) {
		t.Error("operator should be able to deploy routes")
	}
	// Operator has no access to secrets at all.
	if r.Can(op, ActionRead, ResourceSecrets) {
		t.Error("operator should NOT access secrets")
	}
}

func TestRBAC_UndefinedRoleDenied(t *testing.T) {
	r := NewRBAC()
	u := userWith(Role("ghost"))
	if r.Can(u, ActionRead, ResourceRoutes) {
		t.Error("undefined role should grant nothing")
	}
}

func TestRBAC_AddCustomRole(t *testing.T) {
	r := NewRBAC()
	if err := r.AddRole("auditor", Permission{
		Actions:   []Action{ActionRead},
		Resources: []Resource{ResourceSecrets},
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, role := range r.Roles() {
		if role == "auditor" {
			found = true
		}
	}
	if !found {
		t.Error("auditor role should appear in Roles()")
	}
}

func TestRBAC_CustomRoleEvaluatedInCan(t *testing.T) {
	r := NewRBAC()
	_ = r.AddRole("auditor", Permission{
		Actions:   []Action{ActionRead},
		Resources: []Resource{ResourceSecrets},
	})
	u := userWith("auditor")
	if !r.Can(u, ActionRead, ResourceSecrets) {
		t.Error("custom auditor role should grant read on secrets")
	}
	if r.Can(u, ActionWrite, ResourceSecrets) {
		t.Error("custom auditor role should not grant write")
	}
}

func TestRBAC_EmptyRoleNameRejected(t *testing.T) {
	r := NewRBAC()
	if err := r.AddRole("", Permission{}); err == nil {
		t.Error("AddRole with empty name should error")
	}
}

func TestRBAC_ConcurrentCanNoRace(t *testing.T) {
	r := NewRBAC()
	admin := userWith(RoleAdmin)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Can(admin, ActionWrite, ResourceRoutes)
			if err := r.AddRole(Role("r"), Permission{Actions: []Action{ActionRead}}); err != nil {
				t.Errorf("concurrent AddRole: %v", err)
			}
		}()
	}
	wg.Wait()
}
