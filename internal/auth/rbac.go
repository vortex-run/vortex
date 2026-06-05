// Package auth implements VORTEX's authentication and authorization (build plan
// M3.5): a role-based access-control model (org → team → user), API-key issuance
// and verification, OIDC/SSO login, and the HTTP middleware that ties them
// together to protect the management API.
package auth

import (
	"fmt"
	"sync"
)

// Role is a named bundle of permissions assigned to users and API keys.
type Role string

// Built-in roles.
const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
	RoleReadonly Role = "readonly"
)

// Action is an operation a role may be permitted to perform on a resource.
type Action string

// Actions.
const (
	ActionRead   Action = "read"
	ActionWrite  Action = "write"
	ActionDeploy Action = "deploy"
	ActionManage Action = "manage"
)

// Resource is a category of object that actions apply to.
type Resource string

// Resources.
const (
	ResourceRoutes  Resource = "routes"
	ResourceSecrets Resource = "secrets"
	ResourceConfig  Resource = "config"
	ResourceNodes   Resource = "nodes"
)

// allActions and allResources enumerate every defined action/resource, used to
// grant blanket permissions (e.g. admin).
var (
	allActions   = []Action{ActionRead, ActionWrite, ActionDeploy, ActionManage}
	allResources = []Resource{ResourceRoutes, ResourceSecrets, ResourceConfig, ResourceNodes}
)

// Permission defines what a role may do: the set of actions it can perform on
// the set of resources.
type Permission struct {
	Role      Role
	Actions   []Action
	Resources []Resource
}

// User is an authenticated principal in the org → team → user hierarchy.
type User struct {
	ID     string
	Email  string
	OrgID  string
	TeamID string
	Roles  []Role
}

// RBAC holds role definitions and answers authorization questions. It is safe
// for concurrent use.
type RBAC struct {
	mu    sync.RWMutex
	roles map[Role]Permission
}

// NewRBAC returns an RBAC seeded with the built-in role definitions:
//
//	admin    — every action on every resource
//	operator — read+write+deploy on routes, config, nodes
//	viewer   — read on every resource
//	readonly — read on routes and config only
func NewRBAC() *RBAC {
	r := &RBAC{roles: make(map[Role]Permission)}
	r.roles[RoleAdmin] = Permission{
		Role: RoleAdmin, Actions: allActions, Resources: allResources,
	}
	r.roles[RoleOperator] = Permission{
		Role:      RoleOperator,
		Actions:   []Action{ActionRead, ActionWrite, ActionDeploy},
		Resources: []Resource{ResourceRoutes, ResourceConfig, ResourceNodes},
	}
	r.roles[RoleViewer] = Permission{
		Role: RoleViewer, Actions: []Action{ActionRead}, Resources: allResources,
	}
	r.roles[RoleReadonly] = Permission{
		Role:      RoleReadonly,
		Actions:   []Action{ActionRead},
		Resources: []Resource{ResourceRoutes, ResourceConfig},
	}
	return r
}

// Can reports whether any of the user's roles grants action on resource.
func (r *RBAC) Can(user User, action Action, resource Resource) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, role := range user.Roles {
		perm, ok := r.roles[role]
		if !ok {
			continue
		}
		if permitsAction(perm.Actions, action) && permitsResource(perm.Resources, resource) {
			return true
		}
	}
	return false
}

// AddRole adds or replaces a role definition.
func (r *RBAC) AddRole(name Role, perms Permission) error {
	if name == "" {
		return fmt.Errorf("auth: role name must not be empty")
	}
	perms.Role = name
	r.mu.Lock()
	r.roles[name] = perms
	r.mu.Unlock()
	return nil
}

// Roles returns the names of all defined roles.
func (r *RBAC) Roles() []Role {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Role, 0, len(r.roles))
	for name := range r.roles {
		out = append(out, name)
	}
	return out
}

func permitsAction(actions []Action, want Action) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

func permitsResource(resources []Resource, want Resource) bool {
	for _, res := range resources {
		if res == want {
			return true
		}
	}
	return false
}
