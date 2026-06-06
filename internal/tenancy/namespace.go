// Package tenancy implements VORTEX's multi-tenancy layer (build plan M8):
// namespaces that isolate routes, secrets, metrics, and logs per tenant, with
// per-namespace resource quotas enforced at the HTTP and TCP edge. Standard
// library only.
package tenancy

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrQuotaExceeded is returned by CheckQuota when a resource is over its limit.
var ErrQuotaExceeded = errors.New("tenancy: quota exceeded")

// validID allows alphanumerics and hyphens only, so a namespace ID is a safe
// identifier and metric label.
var validID = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// QuotaConfig bounds a namespace's resource use. A zero limit means unlimited.
type QuotaConfig struct {
	MaxRoutes      int   `json:"max_routes"`
	MaxSecrets     int   `json:"max_secrets"`
	MaxConnections int64 `json:"max_connections"`
	BandwidthMbps  int64 `json:"bandwidth_mbps"` // 0 = unlimited
	MaxAgents      int   `json:"max_agents"`     // reserved for the agent system
}

// NamespaceConfig describes a tenant namespace.
type NamespaceConfig struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	OrgID  string      `json:"org_id"`
	Quotas QuotaConfig `json:"quotas"`
}

// Namespace is a tenant isolation boundary with quotas. It is immutable after
// creation except via the Registry's Update.
type Namespace struct {
	cfg NamespaceConfig
}

// NewNamespace validates cfg and constructs a Namespace. ID and OrgID are
// required; ID must be alphanumeric and hyphens only.
func NewNamespace(cfg NamespaceConfig) (*Namespace, error) {
	if cfg.ID == "" {
		return nil, errors.New("tenancy: namespace ID is required")
	}
	if cfg.OrgID == "" {
		return nil, errors.New("tenancy: namespace OrgID is required")
	}
	if !validID.MatchString(cfg.ID) {
		return nil, fmt.Errorf("tenancy: invalid namespace ID %q (alphanumeric and hyphen only)", cfg.ID)
	}
	return &Namespace{cfg: cfg}, nil
}

// ID returns the namespace identifier.
func (n *Namespace) ID() string { return n.cfg.ID }

// Name returns the human-readable namespace name.
func (n *Namespace) Name() string { return n.cfg.Name }

// OrgID returns the owning organization ID.
func (n *Namespace) OrgID() string { return n.cfg.OrgID }

// Quotas returns the namespace's quota configuration.
func (n *Namespace) Quotas() QuotaConfig { return n.cfg.Quotas }

// Config returns a copy of the namespace's full config.
func (n *Namespace) Config() NamespaceConfig { return n.cfg }

// CheckQuota reports whether current usage of resource is within quota. A zero
// limit means unlimited (always nil). It returns an error wrapping
// ErrQuotaExceeded (naming the resource and limit) when over.
//
// resource is one of: "routes", "secrets", "connections", "bandwidth".
func (n *Namespace) CheckQuota(resource string, current int64) error {
	var limit int64
	switch resource {
	case "routes":
		limit = int64(n.cfg.Quotas.MaxRoutes)
	case "secrets":
		limit = int64(n.cfg.Quotas.MaxSecrets)
	case "connections":
		limit = n.cfg.Quotas.MaxConnections
	case "bandwidth":
		limit = n.cfg.Quotas.BandwidthMbps
	default:
		return fmt.Errorf("tenancy: unknown quota resource %q", resource)
	}

	if limit <= 0 {
		return nil // unlimited
	}
	if current >= limit {
		return fmt.Errorf("%w: %s limit %d reached in namespace %q", ErrQuotaExceeded, resource, limit, n.cfg.ID)
	}
	return nil
}
