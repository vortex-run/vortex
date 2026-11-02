package a2a

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DefaultMaxDelegationDepth bounds parent→child subagent fan-out depth.
// Depth 0 is the root task; each child increments depth. Depth >= this value
// is rejected.
const DefaultMaxDelegationDepth = 3

// DefaultMaxChildFanOut bounds how many children a single parent may spawn.
const DefaultMaxChildFanOut = 8

// AgentManifest is a versioned, validated registry entry describing an agent
// for capability-based discovery. It extends AgentCard with permission and
// evaluation metadata without replacing the live A2A card served at runtime.
type AgentManifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Description  string   `json:"description"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
	Tools        []string `json:"tools"`
	// PermissionProfile is an opaque policy label (e.g. "local-workspace",
	// "read-only"). Selection may require an exact match when set on a query.
	PermissionProfile string `json:"permission_profile,omitempty"`
	// MaxBudgetTokens is an optional upper bound advertised for planning; the
	// runtime still enforces real budgets separately.
	MaxBudgetTokens int `json:"max_budget_tokens,omitempty"`
	// EvalNotes documents how this agent is evaluated (not a fake score).
	EvalNotes string `json:"eval_notes,omitempty"`
}

// Catalog is an in-memory, validated registry of agent manifests. It does not
// start processes — discovery is data-only so hundreds of definitions can exist
// without loading them all as live agents.
type Catalog struct {
	byID map[string]AgentManifest
}

// NewCatalog validates and indexes manifests. Duplicate IDs and invalid entries
// fail clearly.
func NewCatalog(manifests []AgentManifest) (*Catalog, error) {
	c := &Catalog{byID: make(map[string]AgentManifest, len(manifests))}
	for i, m := range manifests {
		if err := ValidateManifest(m); err != nil {
			return nil, fmt.Errorf("a2a: manifest[%d]: %w", i, err)
		}
		if _, exists := c.byID[m.ID]; exists {
			return nil, fmt.Errorf("a2a: duplicate agent id %q", m.ID)
		}
		c.byID[m.ID] = m
	}
	return c, nil
}

// ValidateManifest checks required identity and capability fields.
func ValidateManifest(m AgentManifest) error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("at least one capability is required")
	}
	seen := map[string]struct{}{}
	for _, cap := range m.Capabilities {
		cap = strings.TrimSpace(cap)
		if cap == "" {
			return fmt.Errorf("empty capability")
		}
		key := strings.ToLower(cap)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate capability %q", cap)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Get returns a manifest by ID.
func (c *Catalog) Get(id string) (AgentManifest, bool) {
	if c == nil {
		return AgentManifest{}, false
	}
	m, ok := c.byID[id]
	return m, ok
}

// List returns all manifests sorted by ID for deterministic enumeration.
func (c *Catalog) List() []AgentManifest {
	if c == nil {
		return nil
	}
	out := make([]AgentManifest, 0, len(c.byID))
	for _, m := range c.byID {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SelectQuery drives deterministic capability matching before any optional
// semantic ranking (not implemented here).
type SelectQuery struct {
	// RequiredCapabilities must all be present (case-insensitive).
	RequiredCapabilities []string
	// PermissionProfile, when set, must match the manifest exactly.
	PermissionProfile string
	// Role, when set, must match the manifest role exactly.
	Role string
}

// Select returns manifests that satisfy the query, sorted by ID.
func (c *Catalog) Select(q SelectQuery) []AgentManifest {
	if c == nil {
		return nil
	}
	required := make([]string, 0, len(q.RequiredCapabilities))
	for _, r := range q.RequiredCapabilities {
		r = strings.TrimSpace(r)
		if r != "" {
			required = append(required, strings.ToLower(r))
		}
	}
	var out []AgentManifest
	for _, m := range c.byID {
		if q.Role != "" && m.Role != q.Role {
			continue
		}
		if q.PermissionProfile != "" && m.PermissionProfile != q.PermissionProfile {
			continue
		}
		if !hasAllCapabilities(m.Capabilities, required) {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ErrNoMatchingAgent is returned when Select finds no manifest for a query.
var ErrNoMatchingAgent = fmt.Errorf("a2a: no agent matches required capabilities")

// ResolveOrchestration picks the first manifest satisfying q (sorted by ID) and
// returns the orchestration router agent_type for that manifest. Team-only
// manifests (no orchestration route) fail clearly instead of falling back.
func (c *Catalog) ResolveOrchestration(q SelectQuery) (AgentManifest, string, error) {
	matches := c.Select(q)
	if len(matches) == 0 {
		return AgentManifest{}, "", fmt.Errorf("%w (capabilities=%v permission=%q role=%q)",
			ErrNoMatchingAgent, q.RequiredCapabilities, q.PermissionProfile, q.Role)
	}
	m := matches[0]
	route := orchestrationRoute(m.ID)
	if route == "" {
		return AgentManifest{}, "", fmt.Errorf("a2a: agent %q matches selection but is not routable via orchestration (team-only)", m.ID)
	}
	return m, route, nil
}

// ResolveTeam picks the first manifest satisfying q and returns the team pipeline
// role (coder, tester, reviewer) when the manifest is a live team specialist.
func (c *Catalog) ResolveTeam(q SelectQuery) (AgentManifest, string, error) {
	matches := c.Select(q)
	if len(matches) == 0 {
		return AgentManifest{}, "", fmt.Errorf("%w (capabilities=%v permission=%q role=%q)",
			ErrNoMatchingAgent, q.RequiredCapabilities, q.PermissionProfile, q.Role)
	}
	m := matches[0]
	role, ok := teamRole(m.ID)
	if !ok {
		return AgentManifest{}, "", fmt.Errorf("a2a: agent %q matches selection but is not registered on the specialist team", m.ID)
	}
	return m, role, nil
}

func orchestrationRoute(agentID string) string {
	switch agentID {
	case "research-agent":
		return "research"
	case "devops-agent":
		return "devops"
	case "forge-agent":
		return "build_app"
	case "general-agent":
		return "general"
	default:
		return ""
	}
}

func teamRole(agentID string) (string, bool) {
	switch agentID {
	case "code-agent":
		return "coder", true
	case "test-agent":
		return "tester", true
	case "review-agent":
		return "reviewer", true
	default:
		return "", false
	}
}

func hasAllCapabilities(have []string, need []string) bool {
	if len(need) == 0 {
		return true
	}
	set := map[string]struct{}{}
	for _, h := range have {
		set[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	for _, n := range need {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}

// DelegationRequest is the bounded contract for invoking a child agent.
// Children receive only the capabilities listed here — never an implicit
// inheritance of the parent's full permission set.
type DelegationRequest struct {
	ParentTaskID        string
	ChildObjective      string
	AllowedCapabilities []string
	InputEvidence       string
	ExpectedOutput      string
	BudgetTokens        int
	Timeout             time.Duration
	// Depth is the current delegation depth (0 = root). Must be < MaxDepth.
	Depth int
	// MaxDepth defaults to DefaultMaxDelegationDepth when <= 0.
	MaxDepth int
	// SiblingCount is how many children the parent has already spawned.
	SiblingCount int
	// MaxFanOut defaults to DefaultMaxChildFanOut when <= 0.
	MaxFanOut int
}

// ValidateDelegation checks depth/fan-out bounds and required fields. It does
// not execute anything.
func ValidateDelegation(req DelegationRequest) error {
	if strings.TrimSpace(req.ChildObjective) == "" {
		return fmt.Errorf("a2a: child objective is required")
	}
	if len(req.AllowedCapabilities) == 0 {
		return fmt.Errorf("a2a: child must be granted at least one capability")
	}
	maxDepth := req.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDelegationDepth
	}
	if req.Depth < 0 || req.Depth >= maxDepth {
		return fmt.Errorf("a2a: delegation depth %d exceeds max %d", req.Depth, maxDepth)
	}
	maxFan := req.MaxFanOut
	if maxFan <= 0 {
		maxFan = DefaultMaxChildFanOut
	}
	if req.SiblingCount < 0 || req.SiblingCount >= maxFan {
		return fmt.Errorf("a2a: child fan-out %d exceeds max %d", req.SiblingCount, maxFan)
	}
	if req.BudgetTokens < 0 {
		return fmt.Errorf("a2a: budget tokens must be non-negative")
	}
	return nil
}

// ChildCompatible reports whether every capability explicitly granted on the
// delegation request is declared by the child. The child does not inherit the
// parent's broader permission set — only AllowedCapabilities are in scope, and
// they must be capabilities the child actually advertises.
func ChildCompatible(child AgentManifest, req DelegationRequest) error {
	if err := ValidateDelegation(req); err != nil {
		return err
	}
	have := map[string]struct{}{}
	for _, cap := range child.Capabilities {
		have[strings.ToLower(strings.TrimSpace(cap))] = struct{}{}
	}
	for _, g := range req.AllowedCapabilities {
		key := strings.ToLower(strings.TrimSpace(g))
		if key == "" {
			continue
		}
		if _, ok := have[key]; !ok {
			return fmt.Errorf("a2a: child %q does not declare granted capability %q", child.ID, g)
		}
	}
	if req.BudgetTokens > 0 && child.MaxBudgetTokens > 0 && req.BudgetTokens > child.MaxBudgetTokens {
		return fmt.Errorf("a2a: budget %d exceeds child max %d", req.BudgetTokens, child.MaxBudgetTokens)
	}
	return nil
}

// DefaultCatalog returns a small, meaningful initial catalog aligned with
// real VORTEX specialist roles (not hundreds of fake personas).
func DefaultCatalog() *Catalog {
	c, err := NewCatalog([]AgentManifest{
		{
			ID: "code-agent", Name: "VORTEX Code Agent", Role: "coder", Version: "1.0.0",
			Description:       "Implements and edits code in a trusted workspace",
			Capabilities:      []string{"code.edit", "code.read", "workspace.local"},
			Tools:             []string{"read_file", "write_file", "edit_file", "list_directory"},
			PermissionProfile: "local-workspace",
			EvalNotes:         "Covered by specialists package tests and local tool approval tests",
		},
		{
			ID: "test-agent", Name: "VORTEX Test Agent", Role: "tester", Version: "1.0.0",
			Description:       "Runs and interprets tests",
			Capabilities:      []string{"test.run", "code.read", "workspace.local"},
			Tools:             []string{"run_terminal", "read_file", "list_directory"},
			PermissionProfile: "local-workspace",
			EvalNotes:         "Covered by specialists package tests",
		},
		{
			ID: "review-agent", Name: "VORTEX Review Agent", Role: "reviewer", Version: "1.0.0",
			Description:       "Reviews diffs and scores change quality",
			Capabilities:      []string{"review.diff", "code.read"},
			Tools:             []string{"read_file", "git_diff", "git_status"},
			PermissionProfile: "read-mostly",
			EvalNotes:         "Covered by review agent unit tests",
		},
		{
			ID: "research-agent", Name: "VORTEX Research Agent", Role: "researcher", Version: "1.0.0",
			Description:       "Web research with SSRF-safe fetch",
			Capabilities:      []string{"research.web", "research.summarize"},
			Tools:             []string{"http_get"},
			PermissionProfile: "network-ssrf-safe",
			EvalNotes:         "SSRF dialer tests in agents/tools",
		},
		{
			ID: "devops-agent", Name: "VORTEX DevOps Agent", Role: "devops", Version: "1.0.0",
			Description:  "SSH/Docker operations on configured hosts",
			Capabilities: []string{"devops.ssh", "devops.docker"},
			// The devops route drives internal/devops SSH/Docker clients directly;
			// it does not go through the coordinator tool registry, so no registry
			// tool names are advertised here.
			Tools:             []string{},
			PermissionProfile: "devops-hosts",
			EvalNotes:         "Host-key verification covered in devops package",
		},
		{
			ID: "orchestrator-agent", Name: "VORTEX Orchestrator", Role: "coordinator", Version: "1.0.0",
			Description:       "Plans task DAGs and delegates bounded subagents",
			Capabilities:      []string{"plan.dag", "delegate.bounded"},
			Tools:             []string{},
			PermissionProfile: "coordinator",
			EvalNotes:         "orchestration planner/queue tests",
		},
		{
			ID: "forge-agent", Name: "VORTEX Forge", Role: "builder", Version: "1.0.0",
			Description:       "Autonomous app builds in a sandbox",
			Capabilities:      []string{"forge.build", "sandbox.write"},
			Tools:             []string{"write_file", "run_terminal"},
			PermissionProfile: "sandbox",
			EvalNotes:         "forge API tests",
		},
		{
			ID: "general-agent", Name: "VORTEX General", Role: "general", Version: "1.0.0",
			Description:       "Default fallback for unclassified tasks",
			Capabilities:      []string{"general.chat"},
			Tools:             []string{},
			PermissionProfile: "default",
			EvalNotes:         "coordinator classification tests",
		},
	})
	if err != nil {
		// Default manifests are static and must validate; panic is intentional
		// so a broken catalog cannot ship silently.
		panic(err)
	}
	return c
}
